package agent

// Executor-local mirror of the last committed todo/write in the current real
// user turn. Durable state and client projections come from the host event
// ledger; this mirror only gives a running executor immediate semantic access.

import (
	"reasonix/internal/evidence"
)

// SeedTodoState is a test/compatibility helper for constructing an in-memory
// executor projection. Production Plan and Goal paths never call it.
func (a *Agent) SeedTodoState(todos []evidence.TodoItem) {
	if len(todos) == 0 {
		return
	}
	a.setTodoState(todos)
}

// ReplaceTodoState is retained for tests and compatibility callers that need to
// construct an executor-local projection.
func (a *Agent) ReplaceTodoState(todos []evidence.TodoItem) {
	a.setTodoState(todos)
}

// BeginTurnTodoState clears model-managed progress at the real host turn
// boundary. Mid-turn steering, approvals, compaction, and tool rounds do not
// call this method and therefore keep the last successful replacement.
func (a *Agent) BeginTurnTodoState() {
	a.sess.todoMu.Lock()
	a.sess.todoState = nil
	a.sess.todoWritten = false
	a.sess.todoMu.Unlock()
}

// CanonicalTodoState returns a copy of the executor-local task list.
func (a *Agent) CanonicalTodoState() []evidence.TodoItem {
	a.sess.todoMu.Lock()
	defer a.sess.todoMu.Unlock()
	return append([]evidence.TodoItem(nil), a.sess.todoState...)
}

// TodoStateSnapshot returns one coherent semantic projection. TodoWritten is
// true after a successful current-turn write, including an explicit empty
// replacement.
func (a *Agent) TodoStateSnapshot() ([]evidence.TodoItem, bool) {
	a.sess.todoMu.Lock()
	defer a.sess.todoMu.Unlock()
	return append([]evidence.TodoItem(nil), a.sess.todoState...), a.sess.todoWritten
}

// CurrentTaskTodoState returns only the latest successful todo_write retained
// in the current evidence ledger. Unlike CanonicalTodoState, it never falls
// back to a prior user turn.
func (a *Agent) CurrentTaskTodoState() []evidence.TodoItem {
	if a == nil || a.task.ledger == nil {
		return nil
	}
	todos, ok := a.task.ledger.LatestTodos()
	if !ok {
		return nil
	}
	return append([]evidence.TodoItem(nil), todos...)
}
