package cli

import (
	"fmt"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
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
