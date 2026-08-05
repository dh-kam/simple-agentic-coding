package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// summaryHeader separates the original task from the running summary inside
// messages[0]. Resume splits on it to recover both halves; without that, a
// saved-and-restored session would treat "task + summary" as the task and nest
// a fresh header under it on every compaction.
const summaryHeader = "\n\n## 지금까지의 진행 요약\n"

// maybeCompact summarizes older turns when the estimated input exceeds the
// limit. messages[0] (the original task) is kept and rebuilt with a running
// summary; the most recent rounds survive verbatim.
func (a *Agent) maybeCompact(ctx context.Context) error {
	if a.maxContextTokens <= 0 || a.summarizer == nil {
		return nil
	}
	if estimateTokens(a.messages) <= a.maxContextTokens {
		return nil
	}
	// messages[0] is never dropped, so when the opening task alone busts the
	// budget no amount of folding gets under it. Still fold — just without
	// paying for a summary that cannot change the outcome. Refusing outright
	// traded a history that stays a fixed amount over budget for one that grows
	// without limit until the provider rejects it.
	hopeless := estimateTokens(a.messages[:1]) > a.maxContextTokens

	// Give up recent rounds one at a time rather than refusing to compact.
	// Holding keepRecentTurns fixed meant a history whose *newest* rounds
	// alone blew the budget — three large grep results, say — never compacted
	// at all, and every following iteration re-sent an oversized request.
	cutIdx := 0
	for keep := a.keepRecentTurns; keep >= 1 && cutIdx == 0; keep-- {
		cutIdx = planCompaction(a.messages, keep)
	}
	if cutIdx <= 1 {
		return nil // a single round: nothing to fold without breaking pairing
	}

	summary := a.runningSummary
	if hopeless {
		summary = strings.TrimSpace(summary + "\n(맥락 한도를 초과해 이전 " +
			fmt.Sprintf("%d", cutIdx-1) + "개 메시지를 요약 없이 생략했습니다.)")
	} else {
		// Feed the previous summary back in, or each compaction would discard
		// everything the one before it had condensed.
		prefix := make([]ChatMessage, 0, cutIdx)
		if a.runningSummary != "" {
			prefix = append(prefix, ChatMessage{
				Role:    "assistant",
				Content: "이전까지의 진행 요약:\n" + a.runningSummary,
			})
		}
		prefix = append(prefix, a.messages[1:cutIdx]...)

		var err error
		summary, err = a.summarizer(ctx, a.initialUser, prefix)
		if err != nil {
			return err
		}
	}
	a.runningSummary = summary

	kept := make([]ChatMessage, 0, 1+(len(a.messages)-cutIdx))
	kept = append(kept, ChatMessage{Role: "user", Content: a.initialUser + summaryHeader + summary})
	kept = append(kept, a.messages[cutIdx:]...)
	a.messages = kept
	return nil
}

// splitSummary separates a compacted messages[0] back into the original task
// and the running summary. Returns the whole string and "" when it has not
// been compacted yet.
//
// Only for sessions saved before Session carried the two apart — a prompt that
// contains the header text would be split at the wrong point. Matching the
// last occurrence keeps the task intact in the common case, since compaction
// always appends its header at the end.
func splitSummary(content string) (task, summary string) {
	if i := strings.LastIndex(content, summaryHeader); i >= 0 {
		return content[:i], content[i+len(summaryHeader):]
	}
	return content, ""
}

// planCompaction returns the index to cut at: messages[1:cutIdx] gets
// summarized away, messages[cutIdx:] is kept. 0 means "nothing safe to drop".
//
// The cut always lands on a round boundary. A round is one assistant (or
// follow-up user) message plus every tool result answering it, so a round can
// be any length: N parallel tool calls produce 1 + N messages. Cutting
// mid-round would leave a tool_result whose tool_use has been dropped, which
// both the Anthropic and OpenAI APIs reject.
func planCompaction(msgs []ChatMessage, keepRounds int) int {
	if keepRounds < 1 {
		keepRounds = 1
	}
	starts := roundStarts(msgs)
	if len(starts) <= keepRounds {
		return 0
	}
	return starts[len(starts)-keepRounds]
}

// roundStarts lists the indices in msgs[1:] that begin a round — every message
// that is not a tool result.
func roundStarts(msgs []ChatMessage) []int {
	var starts []int
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role != "tool" {
			starts = append(starts, i)
		}
	}
	return starts
}

func defaultSummarizer(backend Backend, model string) Summarizer {
	return func(ctx context.Context, initialUserText string, prefix []ChatMessage) (string, error) {
		msgs := []ChatMessage{{Role: "user", Content: initialUserText}}
		msgs = append(msgs, prefix...)
		resp, err := backend.Chat(ctx, ChatRequest{
			Model:     model,
			MaxTokens: 1024,
			System:    "아래 대화에서 지금까지의 도구 호출과 결과를 바탕으로 핵심 사실·발견·결정만 간결히 요약하라. 이전 요약이 포함되어 있으면 그 내용도 빠짐없이 유지하라.",
			Messages:  msgs,
		}, nil)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
}

func estimateTokens(msgs []ChatMessage) int {
	var n int
	for _, m := range msgs {
		b, _ := json.Marshal(m)
		n += len(b)
	}
	return n / 4
}
