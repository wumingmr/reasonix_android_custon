package control

import (
	"context"
	"fmt"
	"strings"

	"reasonix/internal/session"
)

// ForkTargets reports the fork state of this controller's current session: the
// turns a client may cut at, each with the reason it is or is not forkable yet.
// It reads committed turn records only, so it answers while a turn is running
// and never takes the rotation gate that ForkSession and ForkNamed hold.
func (c *Controller) ForkTargets() (session.ForkTargetSet, error) {
	service, runtime, exclusive := c.v3Binding()
	if !exclusive {
		// The checkpoint engine records message counts rather than turn records, so
		// no boundary can be proven from it: an empty, unverifiable set is the honest
		// answer. A controller with no executor has no session and stays an error.
		if c == nil || c.executor == nil {
			return session.ForkTargetSet{}, fmt.Errorf("checkpoints unavailable")
		}
		return session.ForkTargetSet{Targets: []session.ForkTarget{}, Verifiable: false}, nil
	}
	if service == nil || runtime == nil {
		return session.ForkTargetSet{}, session.ErrSessionNotRunning
	}
	// v3Binding copied the exact service and runtime out from under its own read
	// lock, and SessionRef is immutable, so the source identity is settled before
	// the read below: no controller lock is held across it.
	return service.ForkTargetSetFor(context.Background(), runtime.Ref())
}

// CreateForkSession creates an independent child session from the completed turn
// named by the request, titled name when it is non-empty, and
// returns the child's id. Unlike ForkSession it takes no rotation gate: it runs
// while a turn is in flight, leaves that turn and this controller's own session
// untouched, and publishes no runtime — whichever surface shows the child opens
// it later.
func (c *Controller) CreateForkSession(request session.ForkRequest, name string) (childSessionID string, err error) {
	service, parent, err := c.forkSourceRuntime(request.Source)
	if err != nil {
		return "", err
	}
	// The exact runtime captured above is authoritative, but keep its ref in the
	// request so Service.CreateFork also validates the same immutable source.
	request.Source = parent.Ref()
	result, err := service.CreateFork(context.Background(), request)
	if err != nil {
		return "", err
	}
	// Service.CreateFork publishes the child's durable prefix and returns its
	// identity without opening a runtime, so this call owns no child handle to
	// release; Service.Fork, used by the switching path, opens and closes one.
	if title := strings.TrimSpace(name); title != "" {
		// The child has no runtime here, so the title goes through the service's
		// cold writer: the same session/title event the switching fork appends,
		// flushed under a writer lease this call takes and releases itself.
		if err := service.SetTitle(context.Background(), result.Child, title); err != nil {
			return "", err
		}
	}
	return result.Child.SessionID, nil
}

// forkSourceRuntime resolves the v3 service and the exact live runtime of this
// controller's session. It mirrors branch_ops: sessionEngineEnabled selects the
// v3 engine, and a controller that is not on it — or that is on it without an
// active runtime — has no source identity to fork from.
func (c *Controller) forkSourceRuntime(expected session.SessionRef) (*session.Service, *session.Runtime, error) {
	if c == nil {
		return nil, nil, session.ErrSessionNotRunning
	}
	service, runtime, exclusive := c.v3Binding()
	if !exclusive || service == nil || runtime == nil {
		return nil, nil, session.ErrSessionNotRunning
	}
	if runtime.Ref() != expected {
		return nil, nil, &session.ForkUnavailableError{TurnID: "", Reason: session.ForkStaleSource}
	}
	return service, runtime, nil
}
