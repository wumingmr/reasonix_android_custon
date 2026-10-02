package control

import (
	"context"
	"errors"

	"reasonix/internal/session"
)

func goalRuntimeRetired(c *Controller, service *session.Service, runtime *session.Runtime, settled bool) bool {
	current, ok := service.Runtime(runtime.Ref())
	if settled && !c.Running() && ok && current == runtime {
		if err := service.Close(context.Background(), runtime.Ref()); errors.Is(err, session.ErrRuntimeBusy) || errors.Is(err, session.ErrRuntimeBound) {
			return false
		}
		current, ok = service.Runtime(runtime.Ref())
	}
	return !ok || current != runtime
}
