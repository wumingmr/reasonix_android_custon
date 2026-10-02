package agent

import (
	"testing"

	"reasonix/internal/provider"
)

func TestLegacyOriginFallbackCoversCurrentHostMessageFamilies(t *testing.T) {
	for _, content := range []string{
		CompletionValidationContinuationPrefix + " the last message did not deliver a self-contained final result.",
		StandardTodoContinuationPrefix + " Continue that item now using available tools.",
		emptyFinalRetryMessage(),
		executorHandoffRetryMessage(),
		"This task has reached its token budget. Finalize now.",
		"Your tool-call round limit (max_steps) has been reached.",
		"The following tools are unavailable in the current workflow phase: ask.",
		"Auto recovery has reached its limit for this turn. Summarize now.",
		"Host progress check: the current todo has produced no new completion, unique read, command, or mutation for 8 tool-call rounds. Reassess before using more tools: sign off the current item if it is done, narrow the remaining work without replacing the active item, or explain/ask about a real blocker. Do not repeat reads, commands, or writes just to reset this guard.",
		"Host progress redirect: choose a different approach.",
	} {
		legacy := provider.Message{Role: provider.RoleUser, Content: content}
		if !IsHostGeneratedUserMessage(legacy) {
			t.Errorf("legacy host message was not recognized: %q", content)
		}
	}
}
