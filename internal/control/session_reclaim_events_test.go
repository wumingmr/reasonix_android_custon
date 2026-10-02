package control

import (
	"context"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

// A reclaim releases the CLI's writer and closes the runtime's store; the
// TUI stays alive rendering the conversation. A second /takeover re-opens the
// same identity, and the controller's cached turn-event store must follow the
// new runtime instance — the identity path alone matches, so the stale cache
// would keep answering turn admission from the closed recovery database
// ("database not open").
func TestOpenSessionReopensRuntimeClosedByReclaim(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "ping-pong"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	controller := newOwnedTestController(t, Options{
		Executor: exec, Sink: event.Discard, SessionService: service,
		SessionRuntime: runtime, ExclusiveSession: true,
	})
	// The turn-event store caches the live instance the binding resolved.
	controller.rebindTurnEvents("")
	controller.turnEvents.mu.RLock()
	cached := controller.turnEvents.v3
	controller.turnEvents.mu.RUnlock()
	if cached != runtime.Session() {
		t.Fatal("fixture did not seed the turn-event store cache")
	}

	// The reclaim release: drop every binding, then close the service runtime.
	if err := controller.ReleaseSessionRuntimeBinding(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}

	published, err := controller.OpenSession(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatalf("re-takeover after reclaim failed to re-open the session: %v", err)
	}
	if published != runtime.Ref() {
		t.Fatalf("re-opened ref = %v, want %v", published, runtime.Ref())
	}
	_, reopened, _ := controller.v3Binding()
	if reopened == nil || reopened == runtime {
		t.Fatal("OpenSession kept the closed runtime instead of re-opening")
	}
	// The path that surfaced the bug: the cached store must now be the fresh
	// runtime's session, and a runtime-scoped operation lookup must answer
	// from a live recovery store instead of the closed database.
	controller.turnEvents.mu.RLock()
	recached := controller.turnEvents.v3
	controller.turnEvents.mu.RUnlock()
	if recached != reopened.Session() {
		t.Fatal("turn-event store cache still serves the runtime closed by reclaim")
	}
	if _, err := recached.AppendBatch(t.Context(), "retakeover-probe", []session.Event{{Kind: "diagnostic", Optional: true}}); err != nil {
		t.Fatalf("re-opened runtime store is unusable: %v", err)
	}
}

// A handoff hands the identity to another runtime without allocating a
// replacement: the controller flushes, drops its binding and empties the
// in-memory transcript, so it is back in the never-bound exclusive state. From
// there NewSession must allocate on demand instead of failing on the missing
// runtime, and a Snapshot must be a no-op rather than an "unbound runtime"
// error.
func TestReleaseSessionForHandoffLeavesControllerAllocatable(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	controller := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, ExclusiveSession: true})
	handedOff, err := controller.BindFreshSession(t.Context(), "handed-off")
	if err != nil {
		t.Fatal(err)
	}
	controller.AdoptHistory([]provider.Message{{ID: "u1", Role: provider.RoleUser, Content: "keep me durable"}}, "")
	if msgs := controller.History(); len(msgs) == 0 {
		t.Fatal("fixture did not seed the bound session")
	}

	if err := controller.ReleaseSessionForHandoff(); err != nil {
		t.Fatalf("release for handoff: %v", err)
	}
	if _, bound := controller.SessionRef(); bound {
		t.Fatal("controller still bound after release")
	}
	for _, msg := range controller.History() {
		if msg.Role != provider.RoleSystem {
			t.Fatalf("released controller still carries the handed-off conversation: %+v", msg)
		}
	}
	if err := controller.Snapshot(); err != nil {
		t.Fatalf("snapshot of a released controller must be a no-op, got %v", err)
	}
	// The host closes the runtime; the flushed turn must already be durable.
	if err := service.Close(t.Context(), handedOff); err != nil {
		t.Fatal(err)
	}
	msgs, err := service.Query().History(t.Context(), handedOff)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].Content != "keep me durable" {
		t.Fatalf("handed-off session lost its flushed tail: %+v", msgs)
	}

	if err := controller.NewSession(); err != nil {
		t.Fatalf("NewSession on a released controller: %v", err)
	}
	fresh, bound := controller.SessionRef()
	if !bound || fresh == handedOff {
		t.Fatalf("NewSession bound %+v (bound %v), want a fresh identity", fresh, bound)
	}
}
