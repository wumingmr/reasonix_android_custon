package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestSessionActivityBaselineColdPendingMetadataIsNotComplete(t *testing.T) {
	app, ref := activityBaselineFixture(t, "cold-baseline")
	service := app.desktopSessionService("")
	if err := service.SetTitle(t.Context(), ref, "Cold session"); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	// Freeze only the disposable-cache rebuild scheduler to make a missing cold
	// cache deterministic. Stat remains a header-only read on this service.
	service.Query().Close()
	metadataPath := filepath.Join(app.desktopSessions.root, ".query-cache", ref.SessionID, "catalog-metadata.json")
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	baseline, err := app.GetSessionActivityBaseline(SessionSelector{Ref: &ref})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Complete {
		t.Fatalf("missing cold metadata produced a complete baseline: %+v", baseline)
	}
	if baseline.Ref != ref || baseline.LifecycleGeneration == 0 {
		t.Fatalf("pending observation lost its explicit identity: %+v", baseline)
	}
}

func TestSessionActivityBaselineUsesLiveResultSequenceAndEventVersion(t *testing.T) {
	app, ref := activityBaselineFixture(t, "live-baseline")
	service := app.desktopSessionService("")
	runtime, ok := service.Runtime(ref)
	if !ok {
		t.Fatal("fixture runtime missing")
	}
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "Completed result"}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := runtime.Session().Append(t.Context(), session.Batch{OperationID: "turn", TurnID: "turn", Events: []session.Event{
		{Kind: "turn/start"},
		{Kind: "message/complete", Payload: payload},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resultSequence := commit.FirstSequence + uint64(commit.EventCount) - 1
	// A title event advances the owner event version without creating a result.
	if err := service.SetTitle(t.Context(), ref, "A later title"); err != nil {
		t.Fatal(err)
	}
	baseline, err := app.GetSessionActivityBaseline(SessionSelector{Ref: &ref})
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.Complete || baseline.Ref != ref || baseline.ResultSequence != resultSequence {
		t.Fatalf("live baseline = %+v, want result sequence %d", baseline, resultSequence)
	}
	eventSequence, err := strconv.ParseUint(baseline.EventVersion, 10, 64)
	if err != nil || eventSequence <= baseline.ResultSequence {
		t.Fatalf("event version %q does not distinguish the later title from result %d", baseline.EventVersion, baseline.ResultSequence)
	}
	if baseline.LifecycleGeneration == 0 {
		t.Fatal("live observation omitted lifecycle evidence")
	}
}

func activityBaselineFixture(t *testing.T, id string) (*App, session.SessionRef) {
	t.Helper()
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	app.desktopSessions.root = filepath.Join(root, "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "workspace-state.json"))
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: id, CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, id, ""); err != nil {
		t.Fatal(err)
	}
	return app, runtime.Ref()
}
