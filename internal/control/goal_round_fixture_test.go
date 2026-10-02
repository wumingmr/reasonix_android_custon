package control

import (
	"context"
	"os"
	"testing"

	"reasonix/internal/session"
)

// Round-count semantics must not depend on hundreds of host fsync latencies.
// Failure/restart tests separately exercise real durable writes and recovery.
func goalRoundTestService(t *testing.T) *session.Service {
	t.Helper()
	store, err := session.CreateWithOptions(t.TempDir()+"/goal-unlimited", "goal-unlimited", session.OpenOptions{
		ExternalHistory:        true,
		DisableRecoveryPublish: true,
		Sync:                   func(*os.File) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("desktop", failingFlushPersistence{session: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	return service
}
