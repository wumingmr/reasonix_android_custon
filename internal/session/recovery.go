package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// RecoverInterrupted closes persisted runtime authority that cannot survive a
// process restart. It never reruns a tool or restores an approval. Callers must
// hold the exclusive write handle returned by Open.
//
// This is a Session operation because it derives a closure batch from the
// projection; the physical handle only writes the resulting commit.
func (s *Session) RecoverInterrupted(ctx context.Context) (Commit, bool, error) {
	if s == nil {
		return Commit{}, false, fmt.Errorf("session: nil session")
	}
	if s.readOnly {
		return Commit{}, false, ErrReadOnly
	}
	// Restart recovery needs only live authority, never historical message
	// bodies or the provider workset. Using the full compatibility Snapshot here
	// would replay the entire durable transcript on every cold open.
	snapshot := s.StateSnapshot()
	turnID := snapshot.Projection.TurnID
	if turnID == "" {
		return Commit{}, false, nil
	}
	events := streamCheckpointRecoveryEvents(snapshot.Projection)
	events = append(events, ClosureEvents(snapshot.Projection, "unavailable", "previous runtime exited before recording a result")...)
	terminal, _ := json.Marshal(map[string]any{"status": event.TurnInterrupted})
	events = append(events, Event{Kind: "turn/end", Payload: terminal})
	commit, err := s.Append(ctx, Batch{OperationID: "turn-finalize:" + turnID, TurnID: turnID, Events: events})
	if err != nil {
		return Commit{}, false, err
	}
	if _, err := s.Flush(ctx); err != nil {
		return Commit{}, false, err
	}
	return commit, true, nil
}

// ClosureEvents derives deterministic terminal facts without executing tools
// or changing the provider workset. Live termination and restart share it.
func ClosureEvents(projection Projection, interactionState, detail string) []Event {
	events := make([]Event, 0, len(projection.ActiveTools)+len(projection.Interactions)+len(projection.ActiveSteps))
	toolIDs := make([]string, 0, len(projection.ActiveTools))
	for id := range projection.ActiveTools {
		toolIDs = append(toolIDs, id)
	}
	sort.Strings(toolIDs)
	for _, id := range toolIDs {
		state := provider.ToolRunNotStarted
		legacyState := "not_started"
		if projection.StartedTools[id] {
			state = provider.ToolRunUnknown
			legacyState = "result_unknown"
		}
		payload, _ := json.Marshal(map[string]any{"id": id, "name": projection.ActiveTools[id], "state": legacyState, "runState": state, "error": detail})
		events = append(events, Event{Kind: "tool/result", Payload: payload})
	}
	requestIDs := make([]string, 0, len(projection.Interactions))
	for id := range projection.Interactions {
		requestIDs = append(requestIDs, id)
	}
	sort.Strings(requestIDs)
	for _, id := range requestIDs {
		payload, _ := json.Marshal(map[string]any{"id": id, "state": interactionState})
		events = append(events, Event{Kind: "interaction/resolved", Payload: payload})
	}
	stepIDs := make([]string, 0, len(projection.ActiveSteps))
	for id := range projection.ActiveSteps {
		stepIDs = append(stepIDs, id)
	}
	sort.Strings(stepIDs)
	for _, id := range stepIDs {
		payload, _ := json.Marshal(map[string]any{"id": id})
		events = append(events, Event{Kind: "step/end", Payload: payload})
	}
	return events
}
