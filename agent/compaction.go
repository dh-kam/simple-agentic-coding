package agent

import (
	"context"
	"encoding/json"
)

// maybeCompact summarizes older tool exchanges when the estimated input
// exceeds the limit. Keeps messages[0] (the task, rebuilt with the running
// summary) and the most recent keepRecentTurns exchanges; pairs in between
// are replaced by one summary folded into messages[0].
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

	cutIdx := 1 + summarizePairs*2
	prefix := a.messages[1:cutIdx]

	summary, err := a.summarizer(ctx, a.initialUser, prefix)
	if err != nil {
		return err
	}

	new0 := ChatMessage{Role: "user", Content: a.initialUser + "\n\n## 지금까지의 진행 요약\n" + summary}
	kept := make([]ChatMessage, 0, 1+(len(a.messages)-cutIdx))
	kept = append(kept, new0)
	kept = append(kept, a.messages[cutIdx:]...)
	a.messages = kept
	return nil
}

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

func defaultSummarizer(backend Backend, model string) Summarizer {
	return func(ctx context.Context, initialUserText string, prefix []ChatMessage) (string, error) {
		msgs := []ChatMessage{{Role: "user", Content: initialUserText}}
		msgs = append(msgs, prefix...)
		resp, err := backend.Chat(ctx, ChatRequest{
			Model:     model,
			MaxTokens: 1024,
			System:    "아래 대화에서 지금까지의 도구 호출과 결과를 바탕으로 핵심 사실·발견·결정만 간결히 요약하라.",
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
