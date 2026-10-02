package control

import (
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func (c *Controller) cancelledTurnWasSilent(completion *guardedTurnCompletion) bool {
	if c == nil || c.executor == nil || completion == nil || completion.checkpoint == nil {
		return false
	}
	msgs := c.terminationMessages()
	start := completion.checkpoint.messageIndex
	if start < 0 || start > len(msgs) {
		return false
	}
	for _, m := range msgs[start:] {
		if m.LocalOnly || m.Role == provider.RoleUser || m.Role == provider.RoleSystem {
			continue
		}
		if m.Role == provider.RoleAssistant && (m.Content != "" || m.ReasoningContent != "" || len(m.ToolCalls) > 0) {
			return false
		}
		if m.Role == provider.RoleTool && m.Content != "" {
			return false
		}
	}
	return true
}

func (c *Controller) applyToolRecoveryTurnStatus(done *event.Event, completion *guardedTurnCompletion) {
	if c == nil || c.executor == nil {
		return
	}
	if len(c.executor.PendingToolRecovery()) > 0 {
		// Unknown effects are durable execution facts. They make the cancelled
		// turn diagnosable, but must not create a separate terminal state or an
		// admission requirement for the next turn.
		done.Recovery = &event.RecoveryStatus{State: "unknown", Reason: "tool_effect_unconfirmed"}
		return
	}
	if c.executor.SilentToolRecovery() || c.cancelledTurnWasSilent(completion) {
		done.Recovery = &event.RecoveryStatus{State: "interrupted", Reason: "silent_interruption"}
	}
}
