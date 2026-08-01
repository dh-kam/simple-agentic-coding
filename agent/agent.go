// Package agent implements a tool-use (ReAct) loop over a Backend interface.
// Provider-agnostic: works with Anthropic, OpenAI, or any Backend impl.
// No SDK types leak into the agent loop — only ChatRequest/ChatResponse.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Tool is one capability the agent can invoke.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Run         func(ctx context.Context, args json.RawMessage) (string, error)
}

// Summarizer condenses old conversation turns for compaction.
type Summarizer func(ctx context.Context, initialUserText string, prefix []ChatMessage) (string, error)

// Approver gates a tool call before it runs.
type Approver func(toolName string, args json.RawMessage) (allow bool, reason string)

// ToolStartFunc / ToolEndFunc / ChangeHook — lifecycle hooks.
type ToolStartFunc func(toolUseID, name string, args json.RawMessage)
type ToolEndFunc func(toolUseID, name string, result string, err error)
type ChangeHook func(path, oldContent, newContent string)

// Option configures an Agent.
type Option func(*Agent)

func WithMaxTokens(n int64) Option           { return func(a *Agent) { a.maxTokens = n } }
func WithMaxIterations(n int) Option         { return func(a *Agent) { a.maxIters = n } }
func WithOnText(fn func(string)) Option      { return func(a *Agent) { a.onText = fn } }
func WithPlanner(p Planner) Option           { return func(a *Agent) { a.planner = p } }
func WithOnPlan(fn func(string)) Option      { return func(a *Agent) { a.onPlan = fn } }
func WithMaxContextTokens(n int) Option      { return func(a *Agent) { a.maxContextTokens = n } }
func WithKeepRecentTurns(n int) Option       { return func(a *Agent) { a.keepRecentTurns = n } }
func WithSummarizer(s Summarizer) Option     { return func(a *Agent) { a.summarizer = s } }
func WithApprover(f Approver) Option         { return func(a *Agent) { a.approver = f } }
func WithOnToolStart(f ToolStartFunc) Option { return func(a *Agent) { a.onToolStart = f } }
func WithOnToolEnd(f ToolEndFunc) Option     { return func(a *Agent) { a.onToolEnd = f } }
func WithChangeHook(h ChangeHook) Option     { return func(a *Agent) { a.changeHook = h } }

// Agent drives the tool-use loop. NOT safe for concurrent use.
type Agent struct {
	backend          Backend
	model            string
	system           string
	maxTokens        int64
	maxIters         int
	tools            map[string]Tool
	order            []string
	onText           func(string)
	planner          Planner
	onPlan           func(string)
	maxContextTokens int
	keepRecentTurns  int
	summarizer       Summarizer
	approver         Approver
	onToolStart      ToolStartFunc
	onToolEnd        ToolEndFunc
	changeHook       ChangeHook
	initialUser      string
	totalUsage       Usage
	messages         []ChatMessage
}

func New(backend Backend, model, system string, opts ...Option) *Agent {
	a := &Agent{
		backend:         backend,
		model:           model,
		system:          system,
		maxTokens:       1024,
		maxIters:        25,
		tools:           make(map[string]Tool),
		keepRecentTurns: 3,
	}
	for _, o := range opts {
		o(a)
	}
	if a.summarizer == nil {
		a.summarizer = defaultSummarizer(a.backend, a.model)
	}
	return a
}

func (a *Agent) RegisterTool(t Tool) {
	if _, exists := a.tools[t.Name]; !exists {
		a.order = append(a.order, t.Name)
	}
	a.tools[t.Name] = t
}

func (a *Agent) UnregisterTool(name string) {
	if _, ok := a.tools[name]; !ok {
		return
	}
	delete(a.tools, name)
	for i, n := range a.order {
		if n == name {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
}

func (a *Agent) History() []ChatMessage {
	out := make([]ChatMessage, len(a.messages))
	copy(out, a.messages)
	return out
}

func (a *Agent) Resume(msgs []ChatMessage) {
	a.messages = append(a.messages[:0], msgs...)
}

// Run drives the tool-use loop until the model stops requesting tools.
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	prompt := userInput
	if a.planner != nil {
		plan, err := a.planner.Plan(ctx, userInput)
		if err != nil {
			return "", fmt.Errorf("planning: %w", err)
		}
		if strings.TrimSpace(plan) != "" {
			prompt = userInput + "\n\n## 실행 계획\n" + plan
			if a.onPlan != nil {
				a.onPlan(plan)
			}
		}
	}

	a.initialUser = prompt
	a.messages = append(a.messages, ChatMessage{Role: "user", Content: prompt})

	for iter := 0; iter < a.maxIters; iter++ {
		if err := a.maybeCompact(ctx); err != nil {
			return "", fmt.Errorf("compaction: %w", err)
		}

		resp, err := a.backend.Chat(ctx, ChatRequest{
			Model:     a.model,
			MaxTokens: a.maxTokens,
			System:    a.system,
			Messages:  a.messages,
			Tools:     a.ToolDefs(),
		}, a.onText)
		if err != nil {
			return "", fmt.Errorf("llm call (iter %d): %w", iter, err)
		}

		a.totalUsage.InputTokens += resp.Usage.InputTokens
		a.totalUsage.OutputTokens += resp.Usage.OutputTokens
		a.messages = append(a.messages, resp.ToAssistantMessage())

		if !resp.IsToolUse() {
			return resp.Content, nil
		}

		// Run all tool calls concurrently, feed results back in one message
		results := a.runToolsConcurrently(ctx, resp.ToolCalls)
		a.messages = append(a.messages, results...)
	}
	return "", errors.New("agent: max iterations reached")
}

// runToolsConcurrently executes tool calls in parallel, returns tool result messages.
func (a *Agent) runToolsConcurrently(ctx context.Context, calls []ChatToolCall) []ChatMessage {
	results := make([]ChatMessage, len(calls))
	var wg sync.WaitGroup
	wg.Add(len(calls))
	for i, tc := range calls {
		go func() {
			defer wg.Done()
			results[i] = a.runOneTool(ctx, tc)
		}()
	}
	wg.Wait()
	return results
}

func (a *Agent) runOneTool(ctx context.Context, tc ChatToolCall) ChatMessage {
	if a.onToolStart != nil {
		a.onToolStart(tc.ID, tc.Name, tc.Arguments)
	}
	args := tc.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	if a.approver != nil {
		if allow, reason := a.approver(tc.Name, args); !allow {
			msg := "denied by approver"
			if reason != "" {
				msg += ": " + reason
			}
			if a.onToolEnd != nil {
				a.onToolEnd(tc.ID, tc.Name, msg, errors.New("denied"))
			}
			return ChatMessage{Role: "tool", ToolCallID: tc.ID, Content: msg, IsError: true}
		}
	}

	tool, ok := a.tools[tc.Name]
	var out string
	var err error
	if !ok {
		out = fmt.Sprintf("ERROR: unknown tool %q", tc.Name)
		err = errors.New("unknown tool")
	} else {
		out, err = tool.Run(ctx, args)
	}
	if err != nil {
		out = "ERROR: " + err.Error()
	}
	if a.onToolEnd != nil {
		a.onToolEnd(tc.ID, tc.Name, out, err)
	}
	return ChatMessage{Role: "tool", ToolCallID: tc.ID, Content: out, IsError: err != nil}
}

func (a *Agent) ToolDefs() []ToolDef {
	defs := make([]ToolDef, 0, len(a.order))
	for _, name := range a.order {
		t := a.tools[name]
		defs = append(defs, ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return defs
}

func (a *Agent) TotalUsage() Usage { return a.totalUsage }
