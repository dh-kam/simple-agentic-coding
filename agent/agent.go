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
	usage            *usageCounter
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
	shells           *ShellRegistry
	initialUser      string
	runningSummary   string
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
	// Meter every call that goes through this agent's backend — the main loop,
	// the compaction summarizer, and any subagent handed a.backend all land here.
	a.usage = &usageCounter{inner: a.backend}
	a.backend = a.usage
	if a.summarizer == nil {
		a.summarizer = defaultSummarizer(a.backend, a.model)
	}
	return a
}

// usageCounter wraps a Backend and accumulates token usage across every call
// made through it. Safe for concurrent use: subagents share the counter.
type usageCounter struct {
	inner Backend
	mu    sync.Mutex
	total Usage
}

func (u *usageCounter) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	resp, err := u.inner.Chat(ctx, req, onDelta)
	if resp != nil {
		u.mu.Lock()
		u.total.InputTokens += resp.Usage.InputTokens
		u.total.OutputTokens += resp.Usage.OutputTokens
		u.mu.Unlock()
	}
	return resp, err
}

func (u *usageCounter) Total() Usage {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.total
}

// subagentOptions returns the settings a subagent must inherit from its parent.
// The approval gate is the important one: without it a `task` call would run
// run_command and friends behind the user's back, defeating the permission
// prompt the parent agent is subject to.
func (a *Agent) subagentOptions() []Option {
	opts := []Option{WithMaxTokens(a.maxTokens), WithMaxIterations(a.maxIters)}
	if a.approver != nil {
		opts = append(opts, WithApprover(a.approver))
	}
	if a.onToolStart != nil {
		opts = append(opts, WithOnToolStart(a.onToolStart))
	}
	if a.onToolEnd != nil {
		opts = append(opts, WithOnToolEnd(a.onToolEnd))
	}
	if a.changeHook != nil {
		opts = append(opts, WithChangeHook(a.changeHook))
	}
	if a.maxContextTokens > 0 {
		opts = append(opts, WithMaxContextTokens(a.maxContextTokens), WithKeepRecentTurns(a.keepRecentTurns))
	}
	return opts
}

// Shells exposes the background-shell registry so the host can terminate
// orphaned processes at shutdown. Nil unless the agent was built by
// BuildCodingAssistant.
func (a *Agent) Shells() *ShellRegistry { return a.shells }

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

// Tools returns the currently registered tools, in registration order.
// Resolved lazily by the subagent runner so tools removed after construction
// (config.disable_tools) are removed from sub-tasks as well.
func (a *Agent) Tools() []Tool {
	out := make([]Tool, 0, len(a.order))
	for _, name := range a.order {
		out = append(out, a.tools[name])
	}
	return out
}

func (a *Agent) History() []ChatMessage {
	out := make([]ChatMessage, len(a.messages))
	copy(out, a.messages)
	return out
}

// Session is the persisted form of a conversation. InitialUser and Summary are
// carried explicitly rather than re-parsed out of Messages[0]: compaction folds
// the running summary into that message behind a text header, and recovering it
// by splitting on that header would mangle any prompt that happened to contain
// the same words.
type Session struct {
	Messages    []ChatMessage `json:"messages"`
	InitialUser string        `json:"initial_user,omitempty"`
	Summary     string        `json:"summary,omitempty"`
}

// Snapshot captures the state needed to continue this conversation later.
func (a *Agent) Snapshot() Session {
	return Session{Messages: a.History(), InitialUser: a.initialUser, Summary: a.runningSummary}
}

// Restore continues a saved conversation.
func (a *Agent) Restore(s Session) {
	a.messages = append(a.messages[:0], s.Messages...)
	a.initialUser, a.runningSummary = s.InitialUser, s.Summary
	// Sessions written before Session carried these fields only have the
	// combined text; fall back to splitting it, which is why the header is
	// matched from the end. Guarded on both fields so a session that supplies
	// only Summary does not have it thrown away.
	if a.initialUser == "" && a.runningSummary == "" && len(s.Messages) > 0 && s.Messages[0].Role == "user" {
		a.initialUser, a.runningSummary = splitSummary(s.Messages[0].Content)
	}
}

// Resume continues a conversation from its messages alone.
func (a *Agent) Resume(msgs []ChatMessage) {
	a.Restore(Session{Messages: msgs})
}

// Run drives the tool-use loop until the model stops requesting tools.
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	prompt := userInput
	// Plan only when opening a conversation. Re-planning every follow-up spends
	// an extra LLM round-trip on inputs like "고마워" and the plan is redundant
	// anyway — the earlier one is still in the history.
	if a.planner != nil && len(a.messages) == 0 {
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

	// initialUser anchors compaction. Keep the turn that opened the
	// conversation; overwriting it per turn would make compaction rewrite
	// messages[0] with a later prompt and lose the original task.
	if a.initialUser == "" {
		a.initialUser = prompt
	}
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

// TotalUsage reports the tokens consumed by this agent, including its
// compaction summarizer and any subagents sharing the same backend. An external
// planner has its own backend handle and is not counted here.
func (a *Agent) TotalUsage() Usage { return a.usage.Total() }
