package agent

import (
	"context"

	"reasonix/internal/persistentshell"
	"reasonix/internal/sessiontemp"
)

// withSubagentSessionTemp installs a private temporary directory and persistent
// shell for one sub-agent run. The returned release must be deferred by the
// caller so both are retired when the run ends.
func withSubagentSessionTemp(ctx context.Context) (context.Context, func()) {
	m := sessiontemp.New()
	m.Retain()
	ps := persistentshell.New()
	ps.Retain()
	ctx = sessiontemp.WithManager(ctx, m)
	ctx = persistentshell.WithManager(ctx, ps)
	return ctx, func() {
		ps.Release()
		m.Release()
	}
}
