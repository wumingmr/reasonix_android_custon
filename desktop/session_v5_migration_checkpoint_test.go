package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

func TestMigrationRevisionIgnoresCatalogRebuildButTracksDurableSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte("history"), 0600); err != nil {
		t.Fatal(err)
	}
	files := legacyMigrationSourceFiles(path)
	before, err := desktopMigrationSourceRevision(files)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{store.SessionEventIndex(path), store.SessionDisplayIndex(path), store.SessionTranscriptProjection(path)} {
		if err := os.WriteFile(index, []byte("rebuilt cache"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	after, err := desktopMigrationSourceRevision(files)
	if err != nil || after != before {
		t.Fatalf("catalog rebuild changed source identity: %v", err)
	}
	if err := os.WriteFile(store.SessionMeta(path), []byte(`{"workspace_root":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err = desktopMigrationSourceRevision(files)
	if err != nil || after == before {
		t.Fatalf("ownership change was ignored: %v", err)
	}
}

func TestMigrationCheckpointAllowsProjectionRepairButRejectsHistoryChange(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte("original history"), 0600); err != nil {
		t.Fatal(err)
	}
	meta := store.SessionMeta(path)
	if err := os.WriteFile(meta, []byte(`{"id":"history","workspace_root":"original","future":{"proof":1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cp, err := newDesktopMigrationCheckpoint(desktopMigrationSource{}, "projection-test", legacyMigrationSourceFiles(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta, []byte(`{"id":"history","workspace_root":"original","future":{"proof":1},"turns":1,"schema_version":2,"writer_id":"repair"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cp.complete("target", "content"); err != nil {
		t.Fatalf("projection repair rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cp.complete("target", "content"); err == nil {
		t.Fatal("changed history accepted")
	}
	if err := os.WriteFile(path, []byte("original history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta, []byte(`{"id":"history","workspace_root":"other","future":{"proof":1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cp.complete("target", "content"); err == nil {
		t.Fatal("changed ownership accepted")
	}
}

func appendMigrationTestMessage(t *testing.T, service *session.Service, ref session.SessionRef, id string) {
	t.Helper()
	binding, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: id}})
	if _, err := binding.Runtime().Session().AppendBatch(t.Context(), id, []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
}

func onlyMigrationRecord(t *testing.T) desktopMigrationRecord {
	t.Helper()
	ledger, err := readDesktopMigrationLedger()
	if err != nil || len(ledger.Records) != 1 {
		t.Fatalf("ledger = %#v, %v", ledger, err)
	}
	for _, record := range ledger.Records {
		return record
	}
	panic("unreachable")
}

func TestIncrementalMigrationPreservesContinuedTargetsAndQuarantinesSourceChanges(t *testing.T) {
	for _, kind := range []string{"v4", "legacy"} {
		for _, oldLedger := range []bool{false, true} {
			name := kind + "/current-ledger"
			if oldLedger {
				name = kind + "/old-ledger"
			}
			t.Run(name, func(t *testing.T) {
				isolateDesktopUserDirs(t)
				var migrate func(*App) error
				var growSource func()
				var sourceFile string
				if kind == "v4" {
					root := config.SessionStoreDir()
					old := coldV4MigrationFixture(t, root, "native")
					source := desktopMigrationSource{root: root, scope: "global"}
					migrate = func(app *App) error { return app.migrateCanonicalStore(t.Context(), source) }
					growSource = func() {
						appendMigrationTestMessage(t, old, session.SessionRef{HostID: "migration-source", SessionID: "native"}, "new-source-work")
					}
					sourceFile = filepath.Join(root, "native", "events.frames")
				} else {
					root := config.SessionDir()
					sourceFile = filepath.Join(root, "old.jsonl")
					legacy := agent.NewSession("system")
					legacy.Add(provider.Message{ID: "old", Role: provider.RoleUser, Content: "original work"})
					if err := legacy.Save(sourceFile); err != nil {
						t.Fatal(err)
					}
					source := desktopMigrationSource{root: root, scope: "global", exact: map[string]bool{sourceFile: true}}
					migrate = func(app *App) error {
						err := app.migrateLegacyDirectory(t.Context(), source)
						// Subsequent startups no longer have an open legacy tab.
						source.exact = nil
						return err
					}
					growSource = func() {
						legacy.Add(provider.Message{ID: "new-source-work", Role: provider.RoleUser, Content: "new-source-work"})
						if err := legacy.Save(sourceFile); err != nil {
							t.Fatal(err)
						}
					}
				}
				app := NewApp()
				t.Cleanup(app.closeSessionServices)
				if err := migrate(app); err != nil {
					t.Fatal(err)
				}
				first := onlyMigrationRecord(t)
				if first.SourceRevision == "" || first.Status != "completed" {
					t.Fatalf("missing completed source revision: %#v", first)
				}
				if kind == "legacy" {
					if err := os.Remove(store.SessionMeta(sourceFile)); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
				if oldLedger {
					if err := updateDesktopMigrationLedger(first.SourceKey, first.TargetSessionID, "completed", "", first.ContentDigest); err != nil {
						t.Fatal(err)
					}
				}
				ref := session.SessionRef{HostID: localDesktopHostID, SessionID: first.TargetSessionID}
				appendMigrationTestMessage(t, app.desktopSessionService(""), ref, "new-v5-work")
				if err := app.workspaceRegistry().RenameWorkspace(t.Context(), workspacestate.GlobalWorkspaceID, "user workspace name"); err != nil {
					t.Fatal(err)
				}
				if err := app.workspaceRegistry().SetWorkspaceVisible(t.Context(), workspacestate.GlobalWorkspaceID, false); err != nil {
					t.Fatal(err)
				}
				workspaceBefore, err := os.ReadFile(config.DesktopWorkspaceStatePath())
				if err != nil {
					t.Fatal(err)
				}
				app.closeSessionServices()
				app = NewApp()
				t.Cleanup(app.closeSessionServices)
				for range 2 {
					if err := migrate(app); err != nil {
						t.Fatal(err)
					}
					current := onlyMigrationRecord(t)
					if current.TargetSessionID != first.TargetSessionID || current.Attempts != first.Attempts || current.SourceRevision == "" {
						t.Fatalf("continued target was remigrated: first=%#v current=%#v", first, current)
					}
				}
				// A metadata-only source change must refresh its checkpoint using
				// the recorded source digest, not compare against the continued target.
				stamp := time.Now().Add(-time.Hour)
				if err := os.Chtimes(sourceFile, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				if err := migrate(app); err != nil {
					t.Fatal(err)
				}
				if got := onlyMigrationRecord(t); got.Attempts != first.Attempts || got.TargetSessionID != first.TargetSessionID {
					t.Fatalf("metadata change remigrated content: %#v", got)
				}
				// A failed attempt must retain the previous adoption receipt.
				// Restoring the source after a temporary error cannot resurrect
				// an old copy beside the target that the user has continued.
				if err := updateDesktopMigrationLedger(first.SourceKey, "incomplete-target", "failed", "source_changed", "incomplete-digest"); err != nil {
					t.Fatal(err)
				}
				if err := migrate(app); err != nil {
					t.Fatal(err)
				}
				if got := onlyMigrationRecord(t); got.Status != "completed" || got.TargetSessionID != first.TargetSessionID || got.PreviousCompletion != nil {
					t.Fatalf("previous adoption receipt was lost: %#v", got)
				}
				workspaceAfter, err := os.ReadFile(config.DesktopWorkspaceStatePath())
				if err != nil || !bytes.Equal(workspaceBefore, workspaceAfter) {
					t.Fatalf("completed migration rewrote workspace state: %v", err)
				}
				growSource()
				if err := migrate(app); err != nil {
					t.Fatal(err)
				}
				changed := onlyMigrationRecord(t)
				if changed.TargetSessionID != first.TargetSessionID || changed.ContentDigest != first.ContentDigest {
					t.Fatalf("source change replaced adoption: %#v", changed)
				}
				state, err := app.workspaceRegistry().Load(t.Context())
				if err != nil || len(state.RecoveryEntries) != 1 {
					t.Fatalf("changed source not quarantined: %+v %v", state.RecoveryEntries, err)
				}
				var recoveryID string
				for id := range state.RecoveryEntries {
					recoveryID = id
				}
				// User review creates an independent target; old work remains intact.
				result, err := app.RestoreRecoveryEntry(recoveryID, "review-changed-source")
				if err != nil {
					t.Fatal(err)
				}
				newRef := result.Session
				if newRef == ref {
					t.Fatal("review overwrote continued session")
				}
				for range 2 {
					if err := migrate(app); err != nil {
						t.Fatal(err)
					}
				}
				infos, err := listAllCanonicalSessionInfo(t.Context(), app.desktopSessionService("").Query())
				if err != nil || len(infos) != 2 {
					t.Fatalf("review duplicated source: %#v %v", infos, err)
				}
				history, err := app.desktopSessionService("").Query().History(t.Context(), ref)
				if err != nil || len(history) == 0 || history[len(history)-1].Content != "new-v5-work" {
					t.Fatalf("continued work changed: %#v %v", history, err)
				}
				history, err = app.desktopSessionService("").Query().History(t.Context(), newRef)
				if err != nil || len(history) == 0 || history[len(history)-1].Content != "new-source-work" {
					t.Fatalf("source update lost: %#v %v", history, err)
				}

				before, err := os.ReadFile(desktopMigrationLedgerPath())
				if err != nil {
					t.Fatal(err)
				}
				if err := app.desktopSessionService("").Delete(t.Context(), newRef); err != nil {
					t.Fatal(err)
				}
				// No temporary import is permitted for a completed revision.
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
				if err := migrate(app); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(desktopMigrationLedgerPath())
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("unchanged migration rewrote ledger: %v", err)
				}
				if _, err := os.Stat(filepath.Join(app.desktopSessions.root, newRef.SessionID)); !os.IsNotExist(err) {
					t.Fatalf("deleted target was resurrected: %v", err)
				}
			})
		}
	}
}

func TestMigrationCheckpointRejectsSourceChangeDuringImport(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := newDesktopMigrationCheckpoint(desktopMigrationSource{}, "source", legacyMigrationSourceFiles(path))
	if err != nil {
		t.Fatal(err)
	}
	// An event sidecar can advance while the JSONL checkpoint stays untouched.
	if err := os.WriteFile(store.SessionEventLog(path), []byte("new durable events"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.complete("target", "digest"); err == nil {
		t.Fatal("must not certify a revision that was not the frozen import input")
	}
	record := onlyMigrationRecord(t)
	if record.Status != "failed" || record.ErrorCode != "source_changed" || record.SourceRevision != "" {
		t.Fatalf("source race must remain retryable: %#v", record)
	}
}

func TestMigrationLedgerOptionalRevisionPreservesUnknownFields(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := desktopMigrationLedgerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"version":1,"futureRoot":{"keep":true},"records":{"source":{"sourceKey":"source","targetSessionId":"target","status":"completed","attempts":1,"contentDigest":"digest","futureRecord":"keep"}}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateDesktopMigrationLedger("source", "target", "completed", "", "digest", "stat-v1-revision"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var preserved struct {
		FutureRoot map[string]bool `json:"futureRoot"`
		Records    map[string]struct {
			FutureRecord   string `json:"futureRecord"`
			SourceRevision string `json:"sourceRevision"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &preserved); err != nil || !preserved.FutureRoot["keep"] || preserved.Records["source"].FutureRecord != "keep" || preserved.Records["source"].SourceRevision != "stat-v1-revision" {
		t.Fatalf("optional field update lost data: %s, %v", body, err)
	}
	// The previous reader ignores the optional field while retaining its
	// existing diagnostics/status contract.
	var previous struct {
		Version int `json:"version"`
		Records map[string]struct {
			Status        string `json:"status"`
			ContentDigest string `json:"contentDigest"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &previous); err != nil || previous.Version != 1 || previous.Records["source"].Status != "completed" || previous.Records["source"].ContentDigest != "digest" {
		t.Fatalf("old reader contract changed: %#v, %v", previous, err)
	}
}

func TestMigrationRefusesUnreadableOrFutureLedger(t *testing.T) {
	for _, body := range []string{`{"version":2,"records":{}}`, `{broken`} {
		t.Run(body, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := config.SessionStoreDir()
			coldV4MigrationFixture(t, root, "native")
			path := desktopMigrationLedgerPath()
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			if err := app.migrateCanonicalStore(t.Context(), desktopMigrationSource{root: root, scope: "global"}); err == nil {
				t.Fatal("unknown adoption state must not trigger another import")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != body {
				t.Fatalf("ledger was overwritten: %s, %v", after, err)
			}
			infos, err := listAllCanonicalSessionInfo(t.Context(), app.desktopSessionService("").Query())
			if err != nil || len(infos) != 0 {
				t.Fatalf("import ran despite unreadable ledger: %#v, %v", infos, err)
			}
		})
	}
}
