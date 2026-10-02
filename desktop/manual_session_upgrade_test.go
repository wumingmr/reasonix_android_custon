package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reasonix/desktop/internal/draftstate"
	"reasonix/desktop/internal/workspacestate"
	"testing"
)

func copyRollbackFixture(t *testing.T, version, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "manual-session-upgrade", version, name))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(destination, data, 0600); err != nil {
		t.Fatal(err)
	}
	return destination
}

func TestTaggedWorkspaceFixturesSurviveManualCreationUpgrade(t *testing.T) {
	for _, version := range []string{"1.38.9", "1.38.10", "1.38.11"} {
		t.Run(version, func(t *testing.T) {
			path := copyRollbackFixture(t, version, "workspace-state.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var original map[string]json.RawMessage
			if err := json.Unmarshal(data, &original); err != nil {
				t.Fatal(err)
			}
			original["futureRollbackFixture"] = json.RawMessage(`{"keep":true}`)
			data, _ = json.Marshal(original)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			store := workspacestate.NewStore(path)
			state, err := store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Workspaces["global"].SessionIDs) != 1 || state.Workspaces["global"].SessionIDs[0] != "existing-session" {
				t.Fatalf("history ownership lost: %+v", state)
			}
			if state.PendingCreates["reserved-session"].OperationID != "interrupted-create" {
				t.Fatal("reservation lost")
			}
			if err := store.AttachSession(t.Context(), "interrupted-create", "global", "reserved-session", ""); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(after, []byte("futureRollbackFixture")) {
				t.Fatal("future metadata lost on write")
			}
			reopened, err := workspacestate.NewStore(path).Load(t.Context())
			if err != nil || reopened.Version != 3 || len(reopened.Workspaces["global"].SessionIDs) != 2 {
				t.Fatalf("reopen=%+v %v", reopened, err)
			}
		})
	}
}

func TestTaggedDraftFixturePreservesAllRecoveryIdentities(t *testing.T) {
	for _, version := range []string{"main-v2-pr10469", "main-v2-pr10572", "1.38.11"} {
		t.Run(version, func(t *testing.T) { verifyTaggedDraftRecovery(t, version) })
	}
}

func verifyTaggedDraftRecovery(t *testing.T, version string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	path := copyRollbackFixture(t, version, "drafts.sqlite")
	store := draftstate.New(path)
	defer store.Close()
	settingsOnly, err := store.Get(t.Context(), "draft-settings-only")
	if err != nil || settingsOnly.Status != "active" || !bytes.Contains([]byte(settingsOnly.SettingsJSON), []byte("fixture/model")) {
		t.Fatalf("settings-only draft lost: %+v %v", settingsOnly, err)
	}
	converted, err := store.Get(t.Context(), "draft-converted")
	if err != nil || converted.Status != "converted" {
		t.Fatalf("converted draft revived: %+v %v", converted, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var conflicts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conflicts WHERE draft_id='draft-editable' AND content_json LIKE '%losing historical writer%'`).Scan(&conflicts); err != nil || conflicts != 1 {
		t.Fatalf("historical conflicts=%d %v", conflicts, err)
	}
	for _, phase := range []string{"editable", "reserved", "starting", "dispatching", "dispatching_shell", "dispatch_unknown", "accepted", "cancel_requested", "resume_required", "failed", "cancelled"} {
		draft, err := store.Get(t.Context(), "draft-"+phase)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains([]byte(draft.ContentJSON), []byte("unsent upgrade fixture")) {
			t.Fatal("unsent content lost")
		}
		if phase == "editable" {
			continue
		}
		operation, err := store.Operation(t.Context(), "operation-"+phase)
		if err != nil {
			t.Fatal(err)
		}
		if operation.SessionID != "session-"+phase || operation.TopicID != "topic-"+phase || operation.SubmissionID != "submit-"+phase || operation.Phase != phase {
			t.Fatalf("identity/phase changed: %+v", operation)
		}
		app := NewApp()
		settings, err := app.draftOperationSettings(operation)
		if err != nil || settings.Model != "fixture/frozen" || settings.ToolApprovalMode != "read-only" {
			t.Fatalf("frozen historical snapshot changed: %+v %v", settings, err)
		}
		if !bytes.Contains([]byte(operation.RequestJSON), []byte(`"futureSnapshot":{"preserve":true}`)) {
			t.Fatal("opaque snapshot data lost")
		}
	}
}
