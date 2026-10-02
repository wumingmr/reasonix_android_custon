package bot

// sessionClaim records the decision made while the controller map is locked.
type sessionClaim int

const (
	sessionAbsent sessionClaim = iota
	sessionReused
	sessionChangeDeferred
	sessionRetired
)

// claimSession always releases the gateway lock if a controller method
// panics. Controller calls are expected to be safe, but a failure must not
// wedge later messages and turn cleanup behind gw.mu.
func (gw *BotGateway) claimSession(key string, msg InboundMessage, profile sessionRuntimeProfile) (*sessionState, sessionClaim) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	state, ok := gw.controllers[key]
	switch {
	case !ok:
		return nil, sessionAbsent
	case sessionStateMatchesRuntime(state, profile):
		updateSessionStateRuntime(state, msg, profile)
		return state, sessionReused
	case botSessionHasActiveWork(state):
		return state, sessionChangeDeferred
	default:
		delete(gw.controllers, key)
		return state, sessionRetired
	}
}
