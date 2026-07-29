package agent

import (
	"context"
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
)

// maybeCompact summarizes older tool exchanges when the estimated input
// exceeds the limit. It keeps messages[0] (the task, rebuilt with the running
// summary) and the most recent keepRecentTurns exchanges intact; the pairs in
// between are replaced by one summary folded into messages[0].
//
// Summarizing whole (assistant, tool_result) pairs is what keeps
// tool_use/tool_result pairing valid — a pair is never split.
func (a *Agent) maybeCompact(ctx context.Context) error {
	if a.maxContextTokens <= 0 || a.summarizer == nil {
		return nil
	}
	if estimateTokens(a.messages) <= a.maxContextTokens {
		return nil
	}
	summarizePairs := planCompaction(len(a.messages), a.keepRecentTurns)
	if summarizePairs == 0 {
		return nil
	}

	cutIdx := 1 + summarizePairs*2 // first message index to keep
	prefix := a.messages[1:cutIdx] // pairs to summarize

	summary, err := a.summarizer(ctx, a.initialUser, prefix)
	if err != nil {
		return err
	}

	// Fold the summary into the initial user turn so the (user, asst, user, ...)
	// alternation — and thus re-fireable compaction — stays intact.
	new0 := anthropic.NewUserMessage(anthropic.NewTextBlock(
		a.initialUser + "\n\n## 지금까지의 진행 요약\n" + summary))

	kept := make([]anthropic.MessageParam, 0, 1+(len(a.messages)-cutIdx))
	kept = append(kept, new0)
	kept = append(kept, a.messages[cutIdx:]...)
	a.messages = kept
	return nil
}

// planCompaction returns how many leading (assistant, tool_result) pairs to
// summarize, given the total message count and how many recent pairs to keep.
// messages[0] is the initial user turn; the rest are alternating pairs.
// Returns 0 when there is nothing safe to compact (too few pairs, or an odd
// tail meaning a pair is incomplete).
func planCompaction(messageCount, keepPairs int) int {
	if keepPairs < 1 {
		keepPairs = 1
	}
	pairMsgs := messageCount - 1
	if messageCount < 1 || pairMsgs%2 != 0 {
		return 0
	}
	pairs := pairMsgs / 2
	if pairs <= keepPairs {
		return 0
	}
	return pairs - keepPairs
}

// defaultSummarizer implements Summarizer with one no-tools LLM call.
func defaultSummarizer(c LLMClient, model string) Summarizer {
	return func(ctx context.Context, initialUserText string, prefix []anthropic.MessageParam) (string, error) {
		msgs := make([]anthropic.MessageParam, 0, 1+len(prefix))
		msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(initialUserText)))
		msgs = append(msgs, prefix...)
		resp, err := c.StreamMessage(ctx, anthropic.MessageNewParams{
			Model:     model,
			MaxTokens: 1024,
			System: []anthropic.TextBlockParam{{Text: "아래 대화에서 지금까지의 도구 호출과 결과를 바탕으로 " +
				"핵심 사실·발견·결정만 간결히 요약하라. 코드/명령은 빼고 결과 위주로."}},
			Messages: msgs,
			// no Tools -> the model can only answer with text
		}, nil)
		if err != nil {
			return "", err
		}
		return extractText(resp), nil
	}
}

// estimateTokens is a rough bytes/4 heuristic — accurate enough to decide when
// to compact. The real count is provider-specific and GLM exposes no
// count_tokens endpoint we can rely on.
func estimateTokens(msgs []anthropic.MessageParam) int {
	var n int
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		n += len(b)
	}
	return n / 4
}
