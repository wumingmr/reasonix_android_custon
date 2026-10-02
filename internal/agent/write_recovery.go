package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func (s *Session) addWriteIntent(callID string, raw json.RawMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range slices.Backward(s.Messages) {
		for j, c := range s.Messages[i].ToolCalls {
			if c.ID != callID {
				continue
			}
			calls := append([]provider.ToolCall(nil), s.Messages[i].ToolCalls...)
			calls[j].WriteIntents = append(append([]json.RawMessage(nil), c.WriteIntents...), append(json.RawMessage(nil), raw...))
			s.Messages[i].ToolCalls = calls
			s.version++
			s.rewriteVersion++
			return true
		}
	}
	return false
}

func (a *Agent) withWriteRecovery(ctx context.Context, call provider.ToolCall) context.Context {
	return tool.WithWriteIntentHook(ctx, func(intent tool.FileWriteIntent) error {
		raw, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		if !a.sess.conversation.addWriteIntent(call.ID, raw) {
			return fmt.Errorf("write intent has no durable tool call: %s", call.ID)
		}
		return event.EmitChecked(a.svc.sink, event.Event{Kind: event.Notice, WriteIntent: true})
	})
}

func (a *Agent) verifyInterruptedWrites(ctx context.Context, r *provider.InterruptedTurnRecovery) *provider.InterruptedTurnRecovery {
	// Recovery preserves the original execution facts. Current file contents
	// cannot prove that an earlier call succeeded, and uncertain calls never
	// become execution barriers or synthetic successes.
	return r
}

// A terminal length limit can leave syntactically valid but incomplete args.
func (a *Agent) recordTruncatedToolResults(ctx context.Context, calls []provider.ToolCall) error {
	for _, call := range calls {
		outcome := toolOutcome{output: "error: tool was not executed because the model output reached its length limit; regenerate complete arguments", errMsg: "truncated tool arguments"}
		committedMessage := a.buildBatchToolResult(ctx, call, outcome)
		if err := a.emitBatchToolResult(ctx, call, outcome, committedMessage, 0, 0, false, time.Time{}); err != nil {
			return err
		}
		a.sess.conversation.Add(committedMessage)
	}
	return nil
}
