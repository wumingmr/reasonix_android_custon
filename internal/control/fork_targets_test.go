package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

// newForkTargetsHarness builds an exclusive v3 controller over a filesystem
// service, the harness the other v3 session tests use.
func newForkTargetsHarness(t *testing.T, sessionID string, script ...testutil.Turn) (*session.Service, *session.FilesystemPersistence, *Controller) {
	t.Helper()
	root := t.TempDir()
	persistence := session.NewFilesystemPersistence(filepath.Join(root, "sessions-v4"))
	service, err := session.NewService("desktop", persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(testutil.NewMock(sessionID, script...), tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{
		Runner: exec, Executor: exec, Sink: event.Discard,
		SessionDir:     filepath.Join(root, "legacy"),
		SessionService: service, SessionRuntime: runtime, ExclusiveSession: true,
	})
	return service, persistence, c
}

// sessionDirNames lists the session identities a filesystem service holds. The
// query cache is a read artifact, not a session, so it is excluded.
func sessionDirNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

func TestForkTargetsReportsCompletedTurnAvailable(t *testing.T) {
	_, _, c := newForkTargetsHarness(t, "targets-parent", testutil.Turn{Text: "answer one"}, testutil.Turn{Text: "answer two"})
	if err := c.RunTurn(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := c.RunTurn(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}

	targets, err := c.ForkTargets()
	if err != nil {
		t.Fatal(err)
	}
	if !targets.Verifiable {
		t.Fatal("a session with committed turns must be verifiable")
	}
	if len(targets.Targets) != 2 {
		t.Fatalf("targets = %+v, want both completed turns", targets.Targets)
	}
	for index, target := range targets.Targets {
		if !target.Available || target.Reason != "" {
			t.Fatalf("target %d = %+v, want an available turn", index, target)
		}
		if target.TurnID == "" || target.MessageID == "" {
			t.Fatalf("target %d lost its identity: %+v", index, target)
		}
		if target.TurnNumber != index+1 || target.Status != event.TurnCompleted {
			t.Fatalf("target %d = %+v, want completed turn %d", index, target, index+1)
		}
	}
}

func TestCreateForkSessionCreatesChildWithoutSwitchingController(t *testing.T) {
	service, _, c := newForkTargetsHarness(t, "fork-session-parent", testutil.Turn{Text: "answer one"}, testutil.Turn{Text: "answer two"})
	if err := c.RunTurn(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := c.RunTurn(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	targets, err := c.ForkTargets()
	if err != nil {
		t.Fatal(err)
	}
	parentRef, ok := c.SessionRef()
	if !ok {
		t.Fatal("controller has no v3 session")
	}
	parentPath := c.SessionPath()

	childID, err := c.CreateForkSession(session.ForkRequest{Source: targets.Source, TurnID: targets.Targets[0].TurnID,
		BoundarySequence: targets.Targets[0].BoundarySequence, OperationID: "op-create-child"}, "first turn")
	if err != nil {
		t.Fatal(err)
	}
	if childID == "" || childID == parentRef.SessionID {
		t.Fatalf("child id = %q, parent = %q", childID, parentRef.SessionID)
	}
	if ref, ok := c.SessionRef(); !ok || ref != parentRef {
		t.Fatalf("controller session = %+v, want %+v", ref, parentRef)
	}
	if got := c.SessionPath(); got != parentPath {
		t.Fatalf("controller session path = %q, want %q", got, parentPath)
	}

	childRef := session.SessionRef{HostID: service.HostID(), SessionID: childID}
	if _, opened := service.Runtime(childRef); opened {
		t.Fatal("CreateForkSession published a child runtime")
	}
	childTargets, err := service.ForkTargetSetFor(t.Context(), childRef)
	if err != nil {
		t.Fatal(err)
	}
	if !childTargets.Verifiable || len(childTargets.Targets) != 1 ||
		!childTargets.Targets[0].Available || childTargets.Targets[0].TurnID != targets.Targets[0].TurnID {
		t.Fatalf("child targets = %+v, want the one inherited turn", childTargets)
	}
	childSnapshot, err := service.Query().Snapshot(t.Context(), childRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := childSnapshot.Projection.Title; got != "first turn" {
		t.Fatalf("child title = %q, want %q", got, "first turn")
	}
	if got := childSnapshot.Projection.Messages; len(got) != 3 || got[1].Content != "one" || got[2].Content != "answer one" {
		t.Fatalf("child history = %#v, want the parent prefix through turn one", got)
	}
}

func TestCreateForkSessionRejectsAnchorAfterControllerRebind(t *testing.T) {
	service, persistence, c := newForkTargetsHarness(t, "anchor-parent", testutil.Turn{Text: "answer"})
	if err := c.RunTurn(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	targets, err := c.ForkTargets()
	if err != nil {
		t.Fatal(err)
	}
	target := targets.Targets[0]
	// Seed a second session from the same boundary so it inherits the same turn
	// and message identities. Only the source SessionRef distinguishes it.
	second, err := service.CreateFork(t.Context(), session.ForkRequest{Source: targets.Source, TurnID: target.TurnID,
		BoundarySequence: target.BoundarySequence, OperationID: "seed-second-session"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenSession(t.Context(), second.Child); err != nil {
		t.Fatal(err)
	}
	before := sessionDirNames(t, persistence.Root)
	_, err = c.CreateForkSession(session.ForkRequest{Source: targets.Source, TurnID: target.TurnID,
		BoundarySequence: target.BoundarySequence, OperationID: "stale-source-create"}, "")
	var unavailable *session.ForkUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != session.ForkStaleSource {
		t.Fatalf("rebound create error = %v, want stale_source", err)
	}
	if after := sessionDirNames(t, persistence.Root); !slices.Equal(before, after) {
		t.Fatalf("stale source created a child: %v then %v", before, after)
	}
}

func TestCreateForkSessionUnknownTurnCreatesNoChild(t *testing.T) {
	_, persistence, c := newForkTargetsHarness(t, "unknown-turn-parent", testutil.Turn{Text: "answer"})
	if err := c.RunTurn(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	parentRef, ok := c.SessionRef()
	if !ok {
		t.Fatal("controller has no v3 session")
	}
	before := sessionDirNames(t, persistence.Root)

	childID, err := c.CreateForkSession(session.ForkRequest{Source: parentRef, TurnID: "no-such-turn",
		BoundarySequence: 1, OperationID: "op-unknown-turn"}, "")
	if childID != "" || err == nil {
		t.Fatalf("CreateForkSession(unknown turn) = %q, %v", childID, err)
	}
	var unavailable *session.ForkUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != session.ForkHistoryUnverifiable {
		t.Fatalf("error = %v, want an unverifiable-history refusal", err)
	}
	if after := sessionDirNames(t, persistence.Root); !slices.Equal(before, after) {
		t.Fatalf("refused fork changed session identities: %v then %v", before, after)
	}
	if ref, ok := c.SessionRef(); !ok || ref != parentRef {
		t.Fatalf("controller session = %+v, want %+v", ref, parentRef)
	}
}

func TestCreateForkSessionWorksWhileTurnRunning(t *testing.T) {
	_, _, c := newForkTargetsHarness(t, "running-parent", testutil.Turn{Text: "answer"})
	if err := c.RunTurn(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	parentRef, ok := c.SessionRef()
	if !ok {
		t.Fatal("controller has no v3 session")
	}
	parentPath := c.SessionPath()

	// The harness expresses a live foreground turn the way the rotation tests
	// do; no body runs, so the completed turn below stays committed.
	c.mu.Lock()
	c.turns.phase = session.RuntimeRunning
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.turns.phase = session.RuntimeIdle
		c.mu.Unlock()
	}()

	// The gate this test is about: the switching entry point refuses while the
	// turn runs, so the call below is not passing for a trivial reason.
	if _, err := c.ForkSession(1, ""); err == nil || !strings.Contains(err.Error(), "cannot fork while a turn is running") {
		t.Fatalf("ForkSession while a turn runs = %v, want a running-turn refusal", err)
	}
	targets, err := c.ForkTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Targets) != 1 || !targets.Targets[0].Available {
		t.Fatalf("targets while a turn runs = %+v", targets)
	}

	childID, err := c.CreateForkSession(session.ForkRequest{Source: targets.Source, TurnID: targets.Targets[0].TurnID,
		BoundarySequence: targets.Targets[0].BoundarySequence, OperationID: "op-running-turn"}, "from a running turn")
	if err != nil {
		t.Fatal(err)
	}
	if childID == "" || childID == parentRef.SessionID {
		t.Fatalf("child id = %q, parent = %q", childID, parentRef.SessionID)
	}
	if ref, ok := c.SessionRef(); !ok || ref != parentRef {
		t.Fatalf("controller session = %+v, want %+v", ref, parentRef)
	}
	if got := c.SessionPath(); got != parentPath {
		t.Fatalf("controller session path = %q, want %q", got, parentPath)
	}
}

func TestForkTargetsLegacyEngineReportsUnverifiable(t *testing.T) {
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard})

	targets, err := c.ForkTargets()
	if err != nil {
		t.Fatal(err)
	}
	if targets.Verifiable || len(targets.Targets) != 0 {
		t.Fatalf("legacy targets = %+v, want an empty unverifiable set", targets)
	}
	// The checkpoint engine has no v3 identity, so a cut cannot be requested
	// from it at all.
	if _, err := c.CreateForkSession(session.ForkRequest{TurnID: "turn", BoundarySequence: 1, OperationID: "op-legacy"}, "name"); !errors.Is(err, session.ErrSessionNotRunning) {
		t.Fatalf("legacy CreateForkSession = %v, want ErrSessionNotRunning", err)
	}
}
