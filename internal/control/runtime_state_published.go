package control

import "reasonix/internal/event"

// PublishedRuntimeStateReader is the observation boundary for UI projections.
// It never refreshes owners or waits for a controller, session, or tool lock.
// RuntimeStateReader remains available to callers requiring a fresh sample.
type PublishedRuntimeStateReader interface {
	PublishedRuntimeStateSnapshot() event.RuntimeStateSnapshot
}

// commitSnapshot runs under the producer's sampling lock. Both stored values
// own their slices and nested observations; future samples cannot mutate a
// snapshot already visible to a lock-free reader.
func (r *controllerRuntimeState) commitSnapshot(next event.RuntimeStateSnapshot) {
	r.snapshot = cloneRuntimeState(next)
	committed := cloneRuntimeState(next)
	r.published.Store(&committed)
}

// PublishedRuntimeStateSnapshot returns the last committed immutable state,
// including the initial state before asynchronous notifications begin. Reads
// must remain available while a subsequent producer is waiting on an owner.
func (c *Controller) PublishedRuntimeStateSnapshot() event.RuntimeStateSnapshot {
	if c != nil {
		if state := c.runtimeState.published.Load(); state != nil {
			return cloneRuntimeState(*state)
		}
	}
	return event.RuntimeStateSnapshot{Todos: []event.Todo{}, Interactions: []event.PendingInteraction{}}
}
