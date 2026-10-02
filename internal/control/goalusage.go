package control

import (
	"sync"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/sessioninbox"
)

// goalUsageTee wraps the controller's event sink and attributes billable usage
// events to the admitted automatic goal round. Title generation and unrelated
// background calls are excluded. The tee forwards every event unchanged.
type goalUsageTee struct {
	event.AuditForwarder
	inner          event.Sink
	mu             sync.Mutex
	lifecycleUsage func(event.Event)
}

// NewGoalUsageTee wraps inner in a usage-accounting tee. Pass the returned sink
// to both the agent/executor and the Controller (control.New detects it and
// attaches the goal machine).
func NewGoalUsageTee(inner event.Sink) event.Sink {
	if inner == nil {
		inner = event.Discard
	}
	return &goalUsageTee{AuditForwarder: event.AuditForwarder{Inner: inner}, inner: inner}
}

// Emit forwards the event and, for billable usage while a goal turn is active,
// folds the tokens into the turn recorder.
func (t *goalUsageTee) Emit(e event.Event) {
	if t == nil {
		return
	}
	t.recordUsage(e)
	if t.inner != nil {
		t.inner.Emit(e)
	}
}

// EmitChecked preserves durability-aware sink behavior through the usage tee.
// Prompt and dispatch commits must still fail closed when the inner ledger
// rejects an event.
func (t *goalUsageTee) EmitChecked(e event.Event) error {
	if t == nil {
		return nil
	}
	if err := event.EmitChecked(t.inner, e); err != nil {
		return err
	}
	t.recordUsage(e)
	return nil
}

func (t *goalUsageTee) recordUsage(e event.Event) {
	if e.Kind == event.Usage && e.Usage != nil && e.UsageSource != event.UsageSourceTitle {
		t.mu.Lock()
		observe := t.lifecycleUsage
		t.mu.Unlock()
		if observe != nil {
			observe(e)
		}
	}
}

func (t *goalUsageTee) setLifecycleUsageRecorder(record func(event.Event)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.lifecycleUsage = record
	t.mu.Unlock()
}

func (t *goalUsageTee) InboxChanged(snap sessioninbox.InboxSnapshot) {
	if t == nil {
		return
	}
	notifyInboxChanged(t.inner, snap)
}

// usageTotalTokens prefers TotalTokens and falls back to the non-overlapping
// prompt + completion sum, so cache hit/miss tokens are never double-counted.
func usageTotalTokens(u *provider.Usage) int {
	if u == nil {
		return 0
	}
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}
