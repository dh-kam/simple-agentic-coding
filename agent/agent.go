// Package agent implements a tool-use (ReAct) loop over an Anthropic
// Messages-API compatible client. It is provider-agnostic: point the client
// at first-party Anthropic or any Anthropic-compatible endpoint such as
// GLM Coding Plan (https://open.bigmodel.cn/api/anthropic).
//
// Manual loop tier — plain streaming Messages call + tools, no beta-only
// features — which keeps it compatible with GLM (incl. manual context
// compaction, since GLM has no server-side compaction).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
)

// LLMClient is the seam the loop depends on. StreamMessage drives a streaming
// Messages-API call: it accumulates the full response (so callers still get a
// complete *Message, including StopReason and tool_use blocks) and forwards
// each text delta to onDelta (nil = caller does not want live text).
type LLMClient interface {
	StreamMessage(ctx context.Context, params anthropic.MessageNewParams, onDelta func(string)) (*anthropic.Message, error)
}

// Tool is one capability the agent can invoke. Run receives the model's
// JSON arguments verbatim as json.RawMessage.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON Schema "properties" (type:object is implicit)
	Run         func(ctx context.Context, args json.RawMessage) (string, error)
}

// Summarizer condenses a prefix of the conversation (the initial user turn +
// older assistant/tool exchanges) into short text, used by context compaction.
// Inject a stub in tests; the default uses an LLM call.
type Summarizer func(ctx context.Context, initialUserText string, prefix []anthropic.MessageParam) (string, error)

// Option configures an Agent.
type Option func(*Agent)

// WithMaxTokens sets the per-response output token cap (default 1024).
func WithMaxTokens(n int64) Option { return func(a *Agent) { a.maxTokens = n } }

// WithMaxIterations sets the loop safety cap (default 25).
func WithMaxIterations(n int) Option { return func(a *Agent) { a.maxIters = n } }

// WithOnText registers a callback fired for every streamed text chunk.
func WithOnText(fn func(string)) Option { return func(a *Agent) { a.onText = fn } }

// WithPlanner adds an explicit planning phase before the tool loop.
func WithPlanner(p Planner) Option { return func(a *Agent) { a.planner = p } }

// WithOnPlan registers a callback fired with the generated plan (if any).
func WithOnPlan(fn func(string)) Option { return func(a *Agent) { a.onPlan = fn } }

// WithMaxContextTokens enables context compaction: when the estimated input
// (bytes/4 heuristic) exceeds n tokens, older tool exchanges are summarized
// away. 0 (default) disables compaction.
func WithMaxContextTokens(n int) Option { return func(a *Agent) { a.maxContextTokens = n } }

// WithKeepRecentTurns sets how many recent tool exchanges compaction keeps
// intact (default 3).
func WithKeepRecentTurns(n int) Option { return func(a *Agent) { a.keepRecentTurns = n } }

// WithSummarizer overrides the compaction summarizer (useful in tests).
func WithSummarizer(s Summarizer) Option { return func(a *Agent) { a.summarizer = s } }

// Approver gates a tool call before it runs. Return allow=false to deny; the
// denial (with reason) is fed back to the model as the tool_result so it can
// adapt. nil (default) approves everything.
type Approver func(toolName string, args json.RawMessage) (allow bool, reason string)

// WithApprover installs a gate consulted before each tool_use runs.
func WithApprover(f Approver) Option { return func(a *Agent) { a.approver = f } }

// ToolStartFunc is fired when a tool_use begins dispatching (id, name, args).
type ToolStartFunc func(toolUseID, name string, args json.RawMessage)

// ToolEndFunc is fired when a tool_use finishes (id, name, result, err). err is
// non-nil for denials, unknown tools, and run errors.
type ToolEndFunc func(toolUseID, name string, result string, err error)

// WithOnToolStart installs a hook fired at the start of each tool dispatch.
func WithOnToolStart(f ToolStartFunc) Option { return func(a *Agent) { a.onToolStart = f } }

// WithOnToolEnd installs a hook fired at the end of each tool dispatch.
func WithOnToolEnd(f ToolEndFunc) Option { return func(a *Agent) { a.onToolEnd = f } }

// ChangeHook is fired when a file-modifying tool (write/edit/multi_edit) changes
// a file, with (path, oldContent, newContent). Used by the TUI to render a diff.
type ChangeHook func(path, oldContent, newContent string)

// WithChangeHook installs a file-change observer on file-modifying tools.
func WithChangeHook(h ChangeHook) Option { return func(a *Agent) { a.changeHook = h } }

// Agent drives the tool-use loop. It is NOT safe for concurrent use.
type Agent struct {
	client           LLMClient
	model            string
	system           string
	maxTokens        int64
	maxIters         int
	tools            map[string]Tool
	order            []string // deterministic tool ordering
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
	initialUser      string // original user turn text, for compaction rebuilds
	messages         []anthropic.MessageParam
}

// New builds an Agent. model is any model id the endpoint accepts
// ("claude-opus-5", "glm-5.2", ...).
func New(client LLMClient, model, system string, opts ...Option) *Agent {
	a := &Agent{
		client:          client,
		model:           model,
		system:          system,
		maxTokens:       1024,
		maxIters:        25,
		tools:           make(map[string]Tool),
		keepRecentTurns: 3,
		summarizer:      defaultSummarizer(client, model),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// RegisterTool adds a tool. The last registration of a name wins.
func (a *Agent) RegisterTool(t Tool) {
	if _, exists := a.tools[t.Name]; !exists {
		a.order = append(a.order, t.Name)
	}
	a.tools[t.Name] = t
}

// UnregisterTool removes a tool by name (e.g. to disable it via config).
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

// History returns a copy of the conversation messages (for saving a session).
func (a *Agent) History() []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, len(a.messages))
	copy(out, a.messages)
	return out
}

// Resume seeds the conversation with prior messages, continuing an earlier run.
func (a *Agent) Resume(msgs []anthropic.MessageParam) {
	a.messages = append(a.messages[:0], msgs...)
}

// Run drives the tool-use loop until the model stops requesting tools.
// Maps to docs/01-architecture.md steps ①–⑨.
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	// ① optional explicit planning — inject the plan into the user turn
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

	// ② seed context with the (possibly plan-augmented) user turn
	a.initialUser = prompt
	a.messages = append(a.messages,
		anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))

	for iter := 0; iter < a.maxIters; iter++ { // ③ loop + safety guard
		// compaction: keep context under budget before each call
		if err := a.maybeCompact(ctx); err != nil {
			return "", fmt.Errorf("compaction: %w", err)
		}

		// ④ streamed LLM call (onText surfaces text deltas if set)
		resp, err := a.client.StreamMessage(ctx, anthropic.MessageNewParams{
			Model:     a.model,
			MaxTokens: a.maxTokens,
			System:    []anthropic.TextBlockParam{{Text: a.system}},
			Messages:  a.messages,
			Tools:     a.toolDefs(),
		}, a.onText)
		if err != nil {
			return "", fmt.Errorf("llm call (iter %d): %w", iter, err)
		}

		// ⑤ echo the assistant turn (incl. any tool_use blocks) into history
		a.messages = append(a.messages, resp.ToParam())

		// ⑥⑦ no tool calls -> final answer
		if resp.StopReason != anthropic.StopReasonToolUse {
			return extractText(resp), nil
		}

		// ⑧⑨ run ALL tool_use blocks concurrently, return all results in one turn
		results := a.runToolsConcurrently(ctx, resp.Content)
		a.messages = append(a.messages, anthropic.NewUserMessage(results...))
	}
	return "", errors.New("agent: max iterations reached")
}

// runToolsConcurrently executes every tool_use block in parallel and returns
// the results in the same order as the blocks. The Anthropic API makes parallel
// tool calls by default; returning all tool_result blocks in a single user
// message is what keeps the model doing that (splitting them across messages
// silently trains it to stop).
func (a *Agent) runToolsConcurrently(ctx context.Context, blocks []anthropic.ContentBlockUnion) []anthropic.ContentBlockParamUnion {
	var tus []anthropic.ToolUseBlock
	for _, b := range blocks {
		if tu, ok := b.AsAny().(anthropic.ToolUseBlock); ok {
			tus = append(tus, tu)
		}
	}

	results := make([]anthropic.ContentBlockParamUnion, len(tus))
	var wg sync.WaitGroup
	wg.Add(len(tus))
	for i, tu := range tus { // Go 1.22+: each iteration has its own i, tu
		go func() {
			defer wg.Done()
			results[i] = a.runOneTool(ctx, tu)
		}()
	}
	wg.Wait()
	return results
}

// runOneTool dispatches a single tool_use and returns its tool_result block.
// tool_call_id is matched 1:1 (see docs/02-llm-call.md). The Approver (if any)
// is consulted first; the onToolStart/onToolEnd hooks (if any) bracket the call.
// Denials and errors still go back as tool_result so the model can recover.
func (a *Agent) runOneTool(ctx context.Context, tu anthropic.ToolUseBlock) anthropic.ContentBlockParamUnion {
	args := json.RawMessage(tu.JSON.Input.Raw())
	if a.onToolStart != nil {
		a.onToolStart(tu.ID, tu.Name, args)
	}
	out, err := a.invokeTool(ctx, tu.Name, args)
	if a.onToolEnd != nil {
		a.onToolEnd(tu.ID, tu.Name, out, err)
	}
	return anthropic.NewToolResultBlock(tu.ID, out, err != nil)
}

// invokeTool runs the approver + tool, returning the tool_result content and an
// error (non-nil for denials, unknown tools, and run errors).
func (a *Agent) invokeTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if a.approver != nil {
		if allow, reason := a.approver(name, args); !allow {
			msg := "denied by approver"
			if reason != "" {
				msg += ": " + reason
			}
			return msg, errDenied
		}
	}
	tool, ok := a.tools[name]
	if !ok {
		return fmt.Sprintf("ERROR: unknown tool %q", name), fmt.Errorf("unknown tool %q", name)
	}
	out, err := tool.Run(ctx, args)
	if err != nil {
		return "ERROR: " + err.Error(), err
	}
	return out, nil
}

var errDenied = errors.New("tool call denied by approver")

// toolDefs builds the deterministic tool list sent on every request.
func (a *Agent) toolDefs() []anthropic.ToolUnionParam {
	defs := make([]anthropic.ToolUnionParam, 0, len(a.order))
	for _, name := range a.order {
		t := a.tools[name]
		defs = append(defs, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: t.InputSchema,
				},
			},
		})
	}
	return defs
}

// extractText joins all text blocks in the response.
func extractText(m *anthropic.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}
