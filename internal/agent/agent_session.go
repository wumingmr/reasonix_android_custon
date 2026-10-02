package agent

import "reasonix/internal/fileops"

// InheritFileObservationsFrom is for an idle, same-session runtime rebuild.
// Resume, fork and rewind intentionally use SetSession without this transfer.
func (a *Agent) InheritFileObservationsFrom(previous *Agent) {
	if previous != nil && a != previous {
		a.fileObservations = previous.fileObservations.Clone()
	}
}

// Session returns the agent's current conversation, useful for persistence
// hooks that need to read the message log between turns. sessMu serialises this
// pointer read against SetSession, so a frontend (serve's concurrent /history and
// /new handlers) can't race the swap. The run loop touches a.session directly and
// only swaps it via SetSession while idle, so its reads need no lock.
func (a *Agent) Session() *Session {
	a.sess.mu.Lock()
	defer a.sess.mu.Unlock()
	return a.sess.conversation
}

// SetSession replaces the agent's conversation wholesale. Used by
// `reasonix --resume` to load a saved JSONL transcript before the first turn,
// so the model picks up exactly where it left off. Callers serialise it against a
// running turn (it only fires while idle); sessMu guards the pointer swap itself.
func (a *Agent) SetSession(s *Session) {
	a.sess.reset(s)
	// Observations are live capabilities tied to the exact session instance and
	// execution environment. Never reconstruct or carry them across resume,
	// rewind, fork, or a wholesale session replacement.
	a.fileObservations = fileops.NewStore()
	a.resetPinnedContextState()
	// sessionRuntime.reset clears turn-local Todo state. A resume, fork, rewind
	// or wholesale replacement never reconstructs it from tool messages.
}
