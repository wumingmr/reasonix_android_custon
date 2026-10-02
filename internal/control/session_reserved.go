package control

import (
	"context"
	"errors"
	"reasonix/internal/agent"
	"reasonix/internal/session"
)

// InitializeReservedSession initializes only an explicitly reserved, still empty
// identity. It shares the ordinary creation seed, including the system prompt.
// The Desktop creation owner holds the per-operation lease across this call.
func (c *Controller) InitializeReservedSession(ctx context.Context, ref session.SessionRef) (result error) {
	service, _, _ := c.v3Binding()
	if service == nil {
		return errors.New("session service is unavailable")
	}
	binding, err := service.Open(ctx, ref)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, binding.Release(context.Background())) }()
	runtime := binding.Runtime()
	if len(runtime.Session().ExecutionSnapshot().Projection.ModelMessages) == 0 {
		fresh := agent.NewSession(c.basePrompt())
		if err := seedRuntimeSession(ctx, runtime, "session-create", fresh.Snapshot(), c.ModelRef(), c.ModelSelectionIdentity()); err != nil {
			return err
		}
	}
	_, err = runtime.Session().Flush(ctx)
	return err
}
