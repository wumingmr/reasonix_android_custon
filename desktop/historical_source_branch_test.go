package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/identitylock"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestHistoricalSourceVersionResumesPublishedBranch(t *testing.T) {
	for _, test := range []struct {
		name, format    string
		restart, replay bool
	}{
		{"canonical/retry", "canonical", false, false},
		{"canonical/restart", "canonical", true, false},
		{"canonical/replay", "canonical", true, true},
		{"legacy/retry", "legacy", false, false},
		{"legacy/restart", "legacy", true, false},
		{"legacy/replay", "legacy", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			var changeSource func()
			if test.format == "canonical" {
				old := coldV4MigrationFixture(t, config.SessionStoreDir(), "metadata-update")
				changeSource = func() {
					if err := old.SetTitle(t.Context(), session.SessionRef{HostID: "migration-source", SessionID: "metadata-update"}, "Updated source title"); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err := os.MkdirAll(config.SessionDir(), 0700); err != nil {
					t.Fatal(err)
				}
				path := writeLegacySession(t, config.SessionDir(), "metadata-update.jsonl", "same messages", time.Now())
				changeSource = func() {
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			app := newHistoricalLifecycleApp(t)
			listed, err := app.ListHistoricalSessions()
			if err != nil || len(listed.Items) != 1 {
				t.Fatalf("list: %+v %v", listed, err)
			}
			id := listed.Items[0].ID
			base, err := app.ImportHistoricalSession(id)
			if err != nil {
				t.Fatal(err)
			}
			changeSource()
			_, source, err := app.historicalSourceForSelector(SessionSelector{Ref: &base.Session})
			if err != nil {
				t.Fatal(err)
			}
			update := app.checkHistoricalSourceUpdate(t.Context(), id, source)
			if update.Status != "available" || update.Source == nil {
				t.Fatalf("updated source: %+v", update)
			}
			// Simulate interruption after publishing the branch's content but
			// before publishing its workspace mapping, then retry with either the
			// existing coordinator or a fresh process's durable state.
			app.desktopSessions.beforeMigrationRegistryCommit = func() error { return errors.New("injected registry interruption") }
			prepared, err := app.PrepareHistoricalSourceVersion(*update.Source, update.Version)
			if err != nil {
				t.Fatal(err)
			}
			app.historicalImports.mu.Lock()
			call := app.historicalImports.operations[prepared.OperationID]
			app.historicalImports.mu.Unlock()
			if _, err := waitHistoricalImport(call); err == nil {
				t.Fatal("expected registry interruption")
			}
			failed, err := app.GetSessionPreparation(prepared.OperationID)
			if err != nil || failed.Status != "failed" || !failed.Retryable {
				t.Fatalf("interrupted import: %+v %v", failed, err)
			}
			before, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			pending := pendingHistoricalOperation(before, call.sourceKey)
			if pending == nil || len(pending.SessionIDs) != 1 || pending.Phase != "prepared" {
				t.Fatalf("missing reserved branch: %+v", pending)
			}
			reservedID := pending.SessionIDs[0]
			app.desktopSessions.beforeMigrationRegistryCommit = nil
			if test.restart {
				app.stopHistoricalImports()
				app.closeSessionServices()
				app = newHistoricalLifecycleApp(t)
			}
			if test.replay {
				if err := app.recoverDesktopSessionOperations(t.Context()); err != nil {
					t.Fatalf("explicit recovery of version import: %v", err)
				}
			}
			prepared, err = app.PrepareHistoricalSourceVersion(*update.Source, update.Version)
			if err != nil {
				t.Fatal(err)
			}
			app.historicalImports.mu.Lock()
			call = app.historicalImports.operations[prepared.OperationID]
			app.historicalImports.mu.Unlock()
			result, err := waitHistoricalImport(call)
			if err != nil {
				t.Fatalf("retry of published branch: %v", err)
			}
			if result.Session == base.Session {
				t.Fatalf("explicit branch import reopened the current session: %+v", result.Session)
			}
			if result.Session.SessionID != reservedID {
				t.Fatalf("retry replaced reserved branch %q with %q", reservedID, result.Session.SessionID)
			}
			after, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if after.PendingOperations[pending.ID].Phase != "committed" || len(after.SourceMappings) != 2 {
				t.Fatalf("retry did not commit exactly one branch: %+v", after.SourceMappings)
			}
			unchanged, err := desktopSourceFingerprint(update.Source.Path)
			if err != nil || unchanged != update.Version {
				t.Fatalf("import modified the source: %v", err)
			}
			baseHistory, err := app.desktopSessionService("").Query().History(t.Context(), base.Session)
			if err != nil || len(baseHistory) != 1 {
				t.Fatalf("original conversation lost: %v", err)
			}
			branchHistory, err := app.desktopSessionService("").Query().History(t.Context(), result.Session)
			if err != nil || len(branchHistory) != 1 || branchHistory[0].Content != baseHistory[0].Content {
				t.Fatalf("branch history lost: %+v %v", branchHistory, err)
			}
		})
	}
}

func TestHistoricalSourceVersionOperationDoesNotCollideWithOrdinaryImport(t *testing.T) {
	isolateDesktopUserDirs(t)
	const id = "reserved-source"
	coldV4MigrationFixture(t, config.SessionStoreDir(), id)
	app := newHistoricalLifecycleApp(t)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.SessionStoreDir(), id)
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := desktopMigrationSource{scope: "global"}
	baseOp, err := app.prepareDesktopImport(t.Context(), ordinary, path, fingerprint, "reserved-target", workspace)
	if err != nil {
		t.Fatal(err)
	}
	version := desktopMigrationSource{scope: "global", versionFingerprint: fingerprint}
	branchOp, err := app.prepareDesktopImport(t.Context(), version, path, fingerprint, "reserved-target", workspace)
	if err != nil {
		t.Fatalf("version import collided with ordinary reservation: %v", err)
	}
	if branchOp == baseOp {
		t.Fatal("distinct mappings reused the same operation")
	}
	again, err := app.prepareDesktopImport(t.Context(), version, path, fingerprint, "reserved-target", workspace)
	if err != nil || again != branchOp {
		t.Fatalf("version reservation is not idempotent: %q %v", again, err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingOperations[baseOp].Mapping.SourceKey != ordinary.mappingKey(path) || state.PendingOperations[branchOp].Mapping.SourceKey != version.mappingKey(path) {
		t.Fatal("reservation changed another import's mapping")
	}
}

func TestHistoricalSourceVersionResumesPreviousOperationID(t *testing.T) {
	isolateDesktopUserDirs(t)
	const id = "previous-version-source"
	coldV4MigrationFixture(t, config.SessionStoreDir(), id)
	app := newHistoricalLifecycleApp(t)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.SessionStoreDir(), id)
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	source := desktopMigrationSource{scope: "global", versionFingerprint: fingerprint}
	oldID := "import-" + desktopSourceKey(path, "") + "-" + fingerprint
	if err := app.workspaceRegistry().BeginOperation(t.Context(), workspacestate.Operation{
		ID: oldID, Kind: "import", WorkspaceID: workspace, SessionIDs: []string{"reserved-target"}, Lifecycle: workspacestate.Active,
		Mapping: &workspacestate.SourceMapping{SourceKey: source.mappingKey(path), Path: path, Fingerprint: fingerprint, SessionID: "reserved-target", WorkspaceID: workspace},
	}); err != nil {
		t.Fatal(err)
	}
	opID, err := app.prepareDesktopImport(t.Context(), source, path, fingerprint, "reserved-target", workspace)
	if err != nil || opID != oldID {
		t.Fatalf("previous version reservation replaced: %q %v", opID, err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.PendingOperations) != 1 {
		t.Fatalf("previous version import duplicated: %v", err)
	}
}

// Older migrations could publish another target for an already adopted source
// under its unversioned key. Committing that operation must stay forbidden, but
// it must not prevent an explicit version import from opening a separate branch.
func TestHistoricalSourceVersionWithConflictingContentReadyImport(t *testing.T) {
	for _, scenario := range []string{"ready", "main_head", "target_changed", "target_busy", "source_busy"} {
		t.Run(scenario, func(t *testing.T) { testHistoricalConflictingVersion(t, scenario) })
	}
}

func testHistoricalConflictingVersion(t *testing.T, scenario string) {
	isolateDesktopUserDirs(t)
	const id = "already-adopted"
	old := coldV4MigrationFixture(t, config.SessionStoreDir(), id)
	app := newHistoricalLifecycleApp(t)
	listed, err := app.ListHistoricalSessions()
	if err != nil || len(listed.Items) != 1 {
		t.Fatalf("list: %+v %v", listed, err)
	}
	base, err := app.ImportHistoricalSession(listed.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.SessionStoreDir(), id)
	ordinary := desktopMigrationSource{scope: "global"}
	if scenario == "main_head" {
		ordinary.headID = "main"
		initial, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		mapping := initial.SourceMappings[desktopSourceKey(path, "")]
		mapping.SourceKey, mapping.HeadID = ordinary.mappingKey(path), ordinary.headID
		if err := app.workspaceRegistry().RecordSource(t.Context(), mapping, workspacestate.Presentation{}); err != nil {
			t.Fatal(err)
		}
	}
	oldRef := session.SessionRef{HostID: "migration-source", SessionID: id}
	if err := old.SetTitle(t.Context(), oldRef, "Changed historical title"); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the supplied metadata's old mapping plus content_ready target.
	bundle := filepath.Join(t.TempDir(), "export")
	if err := old.TryExportCold(t.Context(), oldRef, bundle); err != nil {
		t.Fatal(err)
	}
	const reserved = "uncommitted-historical-target"
	if _, err := app.desktopSessionService("").ImportWithHeader(t.Context(), bundle, session.CreateOptions{
		SessionID: reserved, CWD: globalWorkspaceRoot(), Origin: session.SessionOriginCanonicalImport,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.commitDesktopImport(t.Context(), ordinary, path, "canonical", fingerprint, reserved, base.WorkspaceID); !errors.Is(err, workspacestate.ErrMutationConflict) {
		t.Fatalf("conflicting old import = %v, want mutation conflict", err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingHistoricalOperation(state, ordinary.mappingKey(path))
	if pending == nil || pending.Phase != "content_ready" || pending.Mapping.SessionID != reserved {
		t.Fatalf("missing content_ready import: %+v", pending)
	}
	if scenario == "target_changed" {
		ref := session.SessionRef{HostID: localDesktopHostID, SessionID: reserved}
		binding, err := app.desktopSessionService("").Open(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		appendSessionTestMessage(t, binding.Runtime(), "changed", provider.Message{ID: "changed", Role: provider.RoleUser, Content: "Different target history"})
		if err := binding.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := app.desktopSessionService("").Close(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
	}
	// A restart must not turn the pending replacement into permission to
	// overwrite the original source mapping or its continued conversation.
	app.stopHistoricalImports()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	_, source, err := app.historicalSourceForSelector(SessionSelector{Source: &SessionSourceRef{Path: path, HeadID: ordinary.headID}})
	if err != nil {
		t.Fatal(err)
	}
	update := app.checkHistoricalSourceUpdate(t.Context(), ordinary.mappingKey(path), source)
	if update.Status != "available" || update.Source == nil {
		t.Fatalf("updated source: %+v", update)
	}
	request := func() (SessionRestoreResult, error) {
		prepared, err := app.PrepareHistoricalSourceVersion(*update.Source, update.Version)
		if err != nil {
			return SessionRestoreResult{}, err
		}
		app.historicalImports.mu.Lock()
		call := app.historicalImports.operations[prepared.OperationID]
		app.historicalImports.mu.Unlock()
		return waitHistoricalImport(call)
	}
	if scenario == "target_busy" || scenario == "source_busy" {
		var release func()
		if scenario == "source_busy" {
			// Freeze the writer lock without advancing the source generation;
			// a real new generation correctly requires checking updates again.
			release, err = identitylock.TryAcquire(filepath.Join(path, "writer.lock"))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			service := app.desktopSessionService("")
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: reserved}
			binding, err := service.Open(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			release = func() {
				if err := binding.Release(t.Context()); err != nil {
					t.Error(err)
				}
				if err := service.Close(t.Context(), ref); err != nil {
					t.Error(err)
				}
			}
		}
		defer func() {
			if release != nil {
				release()
			}
		}()
		if _, err := request(); !historicalSourceBusyError(err) {
			t.Fatalf("owned session was not blocked: %v", err)
		}
		unchanged, _ := app.workspaceRegistry().Load(t.Context())
		if unchanged.PendingOperations[pending.ID].Phase != "content_ready" || len(unchanged.SourceMappings) != len(state.SourceMappings) {
			t.Fatal("busy recovery changed adoption")
		}
		release()
		release = nil
	}
	result, err := request()
	if scenario == "target_changed" {
		if !errors.Is(err, workspacestate.ErrMutationConflict) {
			t.Fatalf("changed target admitted: %v", err)
		}
		unchanged, _ := app.workspaceRegistry().Load(t.Context())
		if unchanged.PendingOperations[pending.ID].Phase != "content_ready" || len(unchanged.SourceMappings) != len(state.SourceMappings) {
			t.Fatal("changed target recovery modified adoption")
		}
		return
	}
	if err != nil {
		t.Fatalf("version import with conflicting old reservation: %v", err)
	}
	if result.Session == base.Session {
		t.Fatal("branch import reopened the original")
	}
	if result.Session.SessionID != reserved {
		t.Fatal("branch import duplicated the validated content_ready target")
	}
	after, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.SourceMappings[ordinary.mappingKey(path)].SessionID != base.Session.SessionID {
		t.Fatal("branch replaced the original source mapping")
	}
	versionKey := ordinary.mappingKey(path) + ":review:" + fingerprint
	if after.SourceMappings[versionKey].SessionID != result.Session.SessionID {
		t.Fatal("branch version mapping is missing")
	}
	if after.PendingOperations[pending.ID].Phase != "committed" {
		t.Fatal("old operation was not completed")
	}
	app.stopHistoricalImports()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	again, err := request()
	if err != nil || again.Session != result.Session {
		t.Fatalf("restart duplicated or lost recovered branch: %+v %v", again, err)
	}
	for _, ref := range []session.SessionRef{base.Session, result.Session} {
		history, err := app.desktopSessionService("").Query().History(t.Context(), ref)
		if err != nil || len(history) != 1 {
			t.Fatalf("conversation %s unreadable: %v", ref.SessionID, err)
		}
	}
}
