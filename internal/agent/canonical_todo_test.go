package agent

import (
	"testing"

	"reasonix/internal/evidence"
)

func TestTodoStateAllowsParallelAndOutOfOrderStatuses(t *testing.T) {
	a := &Agent{}
	want := []evidence.TodoItem{
		{Content: "later", Status: "completed"},
		{Content: "first", Status: "in_progress"},
		{Content: "second", Status: "in_progress"},
	}
	a.setTodoState(want)
	got := a.CanonicalTodoState()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("todo %d changed: got=%+v want=%+v", i, got[i], want[i])
		}
	}
}

func TestBeginTurnTodoStateClearsWithoutRewritingHistory(t *testing.T) {
	a := &Agent{sess: sessionRuntime{todoState: []evidence.TodoItem{{Content: "old", Status: "in_progress"}}}}
	a.BeginTurnTodoState()
	if got := a.CanonicalTodoState(); len(got) != 0 {
		t.Fatalf("new turn retained todos: %+v", got)
	}
}
