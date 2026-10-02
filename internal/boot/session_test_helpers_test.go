package boot

import (
	"context"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/session"
)

// withTestSession models the production host boundary explicitly. Boot no
// longer creates a persistence service as a side effect of controller assembly.
func withTestSession(t *testing.T, opts Options) Options {
	t.Helper()
	sessionDir := opts.SessionDir
	if sessionDir == "" {
		sessionDir = config.SessionDir()
		opts.SessionDir = sessionDir
	}
	service, err := session.NewService("local", session.NewFilesystemPersistence(session.RootForLegacyDir(sessionDir)))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	opts.SessionService = service
	opts.SessionHostID = "local"
	return opts
}
