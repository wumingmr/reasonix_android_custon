package cli

import (
	"encoding/json"
	"fmt"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func replayTestMessages(n int) []provider.Message {
	messages := make([]provider.Message, 0, n)
	for i := range n {
		messages = append(messages, provider.Message{
			ID:      fmt.Sprintf("m%d", i),
			Role:    provider.RoleUser,
			Content: fmt.Sprintf("body-%d", i),
		})
	}
	return messages
}

func TestClipReplayHistoryKeepsShortHistoryIntact(t *testing.T) {
	messages := replayTestMessages(cliReplayTailMessages)
	got, omitted := clipReplayHistory(messages)
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0 at exactly the tail limit", omitted)
	}
	if len(got) != len(messages) {
		t.Fatalf("kept %d messages, want %d", len(got), len(messages))
	}

	got, omitted = clipReplayHistory(nil)
	if len(got) != 0 || omitted != 0 {
		t.Fatalf("empty history -> %d messages, omitted %d", len(got), omitted)
	}
}

func TestClipReplayHistoryBoundsLongHistory(t *testing.T) {
	messages := replayTestMessages(cliReplayTailMessages * 3)
	got, omitted := clipReplayHistory(messages)
	if got == nil {
		t.Fatal("clipReplayHistory returned nil for a long history")
	}
	if got[len(got)-1].ID != messages[len(messages)-1].ID {
		t.Fatalf("tail does not end at the newest message: %q", got[len(got)-1].ID)
	}
	if omitted+len(got) != len(messages) {
		t.Fatalf("omitted %d + kept %d != %d", omitted, len(got), len(messages))
	}
	if len(got) > cliReplayTailMessages {
		t.Fatalf("kept %d messages, want at most %d", len(got), cliReplayTailMessages)
	}
	// The boundary must land on a user turn, so the replay never opens mid-turn.
	if !agent.IsUserAuthoredTurnMessage(got[0]) {
		t.Fatalf("truncation boundary is not a user turn: %+v", got[0])
	}
}

func TestReplayIndexedTailPreservesOmittedAndTurnBoundary(t *testing.T) {
	_, ctrl, service, _ := newCanonicalTakeoverTUI(t)
	ref, ok := ctrl.SessionRef()
	if !ok {
		t.Fatal("canonical identity missing")
	}
	runtime, ok := service.Runtime(ref)
	if !ok {
		t.Fatal("canonical runtime missing")
	}
	events := make([]session.Event, 0, 600)
	for i := range 600 {
		role := provider.RoleAssistant
		if i%3 == 0 {
			role = provider.RoleUser
		}
		payload, err := json.Marshal(map[string]any{"message": provider.Message{
			ID: fmt.Sprintf("tail-%d", i), Role: role, Content: "body",
		}})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, session.Event{Kind: "message/complete", Payload: payload})
	}
	if _, err := runtime.Session().Append(t.Context(), session.Batch{OperationID: "replay-tail", Events: events}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	full, err := service.Query().History(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	got, omitted := chatUIReplayHistory(ctrl)
	if len(got) == 0 || len(got) > cliReplayTailMessages || !agent.IsUserAuthoredTurnMessage(got[0]) {
		t.Fatalf("indexed replay not bounded/aligned: len=%d", len(got))
	}
	if omitted <= 0 || omitted+len(got) != len(full) {
		t.Fatalf("omitted=%d kept=%d full=%d", omitted, len(got), len(full))
	}
	if got[len(got)-1].ID != "tail-599" {
		t.Fatalf("lost newest message: %s", got[len(got)-1].ID)
	}
}

// When no user turn exists at or below the boundary, clipping must still return
// a bounded slice rather than looping or panicking.
func TestClipReplayHistoryWithoutUserTurns(t *testing.T) {
	messages := make([]provider.Message, cliReplayTailMessages*2)
	for i := range messages {
		messages[i] = provider.Message{ID: fmt.Sprintf("a%d", i), Role: provider.RoleAssistant, Content: "prose"}
	}
	got, omitted := clipReplayHistory(messages)
	if len(got) != cliReplayTailMessages {
		t.Fatalf("kept %d messages, want %d", len(got), cliReplayTailMessages)
	}
	if omitted+len(got) != len(messages) {
		t.Fatalf("omitted %d + kept %d != %d", omitted, len(got), len(messages))
	}
}
