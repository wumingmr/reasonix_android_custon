package control

import (
	"reasonix/internal/event"
	"sync"
	"time"
)

var diagnosticProcessStartedAt = time.Now().UTC()

type lifecycleDiagnostic struct {
	ObservedAt time.Time        `json:"observedAt"`
	Action     string           `json:"action"`
	Source     string           `json:"source"`
	TurnID     string           `json:"turnId,omitempty"`
	Sequence   uint64           `json:"sequence,omitempty"`
	State      event.TurnStatus `json:"state,omitempty"`
}
type lifecycleDiagnosticBuffer struct {
	mu      sync.Mutex
	events  []lifecycleDiagnostic
	dropped uint64
}

func (c *Controller) recordLifecycle(action, source, turnID string, sequence uint64, status event.TurnStatus) {
	if c == nil {
		return
	}
	buffer := &c.lifecycleDiagnostics
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(buffer.events) == 256 {
		copy(buffer.events, buffer.events[1:])
		buffer.events = buffer.events[:255]
		buffer.dropped++
	}
	buffer.events = append(buffer.events, lifecycleDiagnostic{ObservedAt: time.Now().UTC(), Action: action, Source: source, TurnID: turnID, Sequence: sequence, State: status})
}
func (c *Controller) lifecycleDiagnosticSnapshot() any {
	buffer := &c.lifecycleDiagnostics
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	events := append([]lifecycleDiagnostic{}, buffer.events...)
	return struct {
		ProcessStartedAt time.Time             `json:"processStartedAt"`
		Events           []lifecycleDiagnostic `json:"events"`
		Dropped          uint64                `json:"dropped"`
		Scope            string                `json:"scope"`
	}{diagnosticProcessStartedAt, events, buffer.dropped, "current controller lifetime; earlier and discarded observations are unavailable"}
}

func (c *Controller) recordTurnLifecycle(e event.Event) {
	if e.Kind == event.TurnStarted || e.Kind == event.TurnDone || e.Kind == event.TurnStatusChanged {
		c.recordLifecycle("turn_status", "controller", e.TurnID, e.Sequence, e.Status)
	}
}
