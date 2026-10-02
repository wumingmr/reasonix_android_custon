package control

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func canonicalIdentityController(t *testing.T, service *session.Service, id, legacy string, manager *jobs.Manager) *Controller {
	t.Helper()
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	return newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, Jobs: manager,
		SessionService: service, SessionRuntime: runtime, ExclusiveSession: true, SessionPath: legacy})
}

func TestCanonicalInboxWithLegacyImportPathUsesTemporaryStorage(t *testing.T) {
	t.Chdir(t.TempDir())
	service, err := session.NewService("local", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "old.jsonl")
	c := canonicalIdentityController(t, service, "imported.session", legacy, nil)
	st, err := c.ensureInbox()
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionPath() != "session-id:imported.session" || !filepath.IsAbs(st.Dir()) {
		t.Fatalf("mixed locator and storage path: locator=%q directory=%q", st.SessionPath(), st.Dir())
	}
	if _, err := os.Stat("session-id:imported.session.inbox"); !os.IsNotExist(err) {
		t.Fatalf("canonical locator became a file path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(legacy), "old.inbox")); !os.IsNotExist(err) {
		t.Fatalf("canonical inbox wrote beside the import source: %v", err)
	}
	receipt, err := c.EnqueueInbox(InboxRequest{ExpectedSessionPath: "session-id:imported.session", Submit: "later"})
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := c.ReadInboxItem(receipt.ItemID)
	if err != nil || meta.SessionID != "imported.session" {
		t.Fatalf("queue item lost its exact canonical ID: %+v %v", meta, err)
	}
}

func TestCanonicalRuntimeJobsAndPermissionSnapshotsUseExactSessionIdentity(t *testing.T) {
	service, err := session.NewService("local", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	manager := jobs.NewManager(event.Discard)
	t.Cleanup(manager.Close)
	a := canonicalIdentityController(t, service, "a", "", manager)
	b := canonicalIdentityController(t, service, "b", "", manager)
	empty := canonicalIdentityController(t, service, "empty", "", manager)
	start := func(id string) *jobs.Job {
		return manager.StartForSession(id, "bash", id, func(ctx context.Context, _ io.Writer) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
	}
	jobA, jobB := start("a"), start("b")
	for _, tc := range []struct {
		c     *Controller
		id    string
		count int
	}{{a, "a", 1}, {b, "b", 1}, {empty, "empty", 0}} {
		if got := tc.c.RuntimeStateSnapshot().BackgroundJobs; got != tc.count {
			t.Errorf("session %s counted %d jobs, want %d", tc.id, got, tc.count)
		}
		if got := tc.c.PermissionSnapshot().SessionID; got != tc.id {
			t.Errorf("session %s permission identity = %q", tc.id, got)
		}
	}
	if a.CancelJob(jobB.ID) || !a.CancelJob(jobA.ID) || !b.CancelJob(jobB.ID) {
		t.Fatal("job cancellation crossed its session boundary")
	}
}
