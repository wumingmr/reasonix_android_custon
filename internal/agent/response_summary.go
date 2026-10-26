package agent

import "reasonix/internal/event"

func (a *Agent) responseSummary(s streamedTurn) *event.ResponseSummary {
	summary := &event.ResponseSummary{
		Provider: a.svc.prov.Name(), ModelRef: a.modelRef,
		FinishReason: "unknown", TextBytes: len(s.text), ReasoningBytes: len(s.reasoning),
		ToolCalls: len(s.calls), ServerSearches: len(s.serverSearch),
		StreamComplete: s.err == nil && !s.interrupted,
	}
	if a.turn.terminal.answeringRecovery {
		summary.RecoveryAttempts = a.turn.terminal.emptyFinalBlocks
	}
	if u := s.usage; u != nil {
		if u.FinishReason != "" {
			summary.FinishReason = u.FinishReason
		}
		summary.Usage = &event.ResponseUsage{
			InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens,
			ReasoningTokens: u.ReasoningTokens, RequestCount: usageRequestCount(u),
			Estimated: u.Estimated, Unknown: u.Unknown,
		}
	}
	return summary
}
