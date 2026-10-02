package agent

import (
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestRepeatReminderAtThreeFiveEightNeverBlocks(t *testing.T) {
	a := &Agent{}
	call := provider.ToolCall{Name: "read_file", Arguments: `{"limit":1,"path":"secret.txt"}`}
	for count := 1; count <= 8; count++ {
		results := []string{"ok"}
		a.applyRepeatReminders([]provider.ToolCall{call}, results)
		want := count == 3 || count == 5 || count == 8
		if got := strings.Contains(results[0], "[repeat reminder]"); got != want {
			t.Fatalf("count %d reminder=%v want=%v: %q", count, got, want, results[0])
		}
		if strings.Contains(results[0], "secret.txt") {
			t.Fatal("reminder copied sensitive arguments")
		}
	}
}

func TestRepeatReminderCanonicalizesJSONAndResetsOnChange(t *testing.T) {
	a := &Agent{}
	for _, args := range []string{`{"path":"a","limit":1}`, `{"limit":1,"path":"a"}`, `{"path":"a", "limit":1}`} {
		results := []string{"ok"}
		a.applyRepeatReminders([]provider.ToolCall{{Name: "read_file", Arguments: args}}, results)
	}
	if a.turn.repeatCount != 3 {
		t.Fatalf("count=%d", a.turn.repeatCount)
	}
	a.applyRepeatReminders([]provider.ToolCall{{Name: "read_file", Arguments: `{"path":"b"}`}}, []string{"ok"})
	if a.turn.repeatCount != 1 {
		t.Fatalf("changed call did not reset: %d", a.turn.repeatCount)
	}
}
