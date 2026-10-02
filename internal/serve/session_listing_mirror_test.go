package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
)

// TestSessionsFoldsEngineMirrorOfLegacyTranscript pins the listing fold: the
// engine mirrors an in-flight legacy transcript into a final-format event log
// keyed by the legacy branch id; the /sessions view must keep one row for that
// conversation instead of a transcript row plus its mirror.
func TestSessionsFoldsEngineMirrorOfLegacyTranscript(t *testing.T) {
	legacyDir := t.TempDir()
	legacy := filepath.Join(legacyDir, "mirror-me.jsonl")
	if err := os.WriteFile(legacy, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v4Root := filepath.Join(t.TempDir(), "sessions-v4")
	persistence := session.NewFilesystemPersistence(v4Root)
	mirror, err := persistence.Create(session.CreateOptions{SessionID: "mirror-me"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mirror.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("serve-test", persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown session service: %v", err)
		}
	})
	exec := agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Executor: exec, SessionDir: legacyDir, SessionService: service, ExclusiveSession: true})
	if _, err := ctrl.BindFreshSession(t.Context(), "foreground"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctrl.Close)
	srv := newLifecycleTestServer(t, ctrl, NewBroadcaster(), config.ServeConfig{})
	recorder := httptest.NewRecorder()
	srv.sessions(recorder, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	var rows []sessionListEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	legacyKey := agent.CanonicalSessionPath(legacy)
	sawLegacy, sawMirror := false, false
	for _, row := range rows {
		if agent.CanonicalSessionPath(row.Path) == legacyKey {
			sawLegacy = true
		}
		if row.SessionID == "mirror-me" {
			sawMirror = true
		}
	}
	if !sawLegacy {
		t.Fatalf("legacy row missing from listing: %+v", rows)
	}
	if sawMirror {
		t.Fatalf("engine mirror listed beside its transcript: %+v", rows)
	}
	retireExclusiveForeground(t, ctrl, service)
}
