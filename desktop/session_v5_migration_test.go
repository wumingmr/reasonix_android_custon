package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// These fixtures have no Desktop header or open tab, just like v4-only
// conversations left behind after a downgrade. Their catalog cache is absent.
func coldV4MigrationFixture(t *testing.T, root, id string) *session.Service {
	t.Helper()
	service, err := session.NewService("migration-source", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(t.Context()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "user", Role: provider.RoleUser, Content: "恢复完整对话"}})
	if _, err := runtime.Session().AppendBatch(t.Context(), "message", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".query-cache")); err != nil {
		t.Fatal(err)
	}
	info, err := session.NewFilesystemPersistence(root).Stat(t.Context(), id)
	if err != nil || info.MetadataStatus != session.MetadataPending || info.Turns != 0 || info.Title != "" || info.Preview != "" {
		t.Fatalf("fixture must reproduce cold, empty display metadata: %#v, %v", info, err)
	}
	return service
}

func TestDesktopV5ExplicitImportMigratesColdV4WithoutLegacyOrOpenTab(t *testing.T) {
	for _, scope := range []string{"project", "global"} {
		t.Run(scope, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			workspace := filepath.Join(t.TempDir(), "中文项目")
			if err := os.MkdirAll(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			sourceRoot := config.SessionStoreDir()
			if scope == "project" {
				sourceRoot = config.ProjectSessionStoreDir(workspace)
				if err := saveProjectsFile(desktopProjectFile{Projects: []desktopProject{{Root: workspace}}}); err != nil {
					t.Fatal(err)
				}
			}
			const id = "v4-native"
			coldV4MigrationFixture(t, sourceRoot, id)
			original := map[string][]byte{}
			for _, name := range []string{"manifest.json", "events.frames"} {
				body, err := os.ReadFile(filepath.Join(sourceRoot, id, name))
				if err != nil {
					t.Fatal(err)
				}
				original[name] = body
			}
			for attempt := range 2 {
				app := NewApp()
				t.Cleanup(app.closeSessionServices)
				if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
					t.Fatal(err)
				}
				workspaceID := workspacestate.GlobalWorkspaceID
				if scope == "project" {
					workspaceID = desktopWorkspaceID(scope, workspace)
				}
				state, err := app.workspaceRegistry().Load(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if ids := state.Workspaces[workspaceID].SessionIDs; len(ids) != 1 || ids[0] != id {
					t.Fatalf("attempt %d: cold v4 membership = %v", attempt, ids)
				}
				page, err := app.ReadSessionHistory(session.SessionRef{HostID: localDesktopHostID, SessionID: id}, "", 10)
				if err != nil || len(page.Messages) != 1 || page.Messages[0].Content != "恢复完整对话" {
					t.Fatalf("restored history = %#v, %v", page, err)
				}
				diagnostics, err := app.GetSessionArchitectureDiagnostics()
				if err != nil || diagnostics.MigrationCompleted != 1 || diagnostics.SessionHeadersTotal != 1 {
					t.Fatalf("migration diagnostics = %#v, %v", diagnostics, err)
				}
				app.closeSessionServices()
			}
			for name, before := range original {
				after, err := os.ReadFile(filepath.Join(sourceRoot, id, name))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("source %s changed: %v", name, err)
				}
			}
			info, err := session.NewFilesystemPersistence(sourceRoot).Stat(t.Context(), id)
			if err != nil || info.MetadataStatus != session.MetadataPending {
				t.Fatalf("migration must not rebuild source display metadata: %#v, %v", info, err)
			}
		})
	}
}

func TestCanonicalV4MigrationReportsSourceFailureAndRetriesAfterRepair(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	coldV4MigrationFixture(t, root, "healthy")
	coldV4MigrationFixture(t, root, "damaged")
	manifestPath := filepath.Join(root, "damaged", "manifest.json")
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err == nil {
		t.Fatal("damaged source must not silently disappear")
	}
	diagnostics, err := app.GetSessionArchitectureDiagnostics()
	if err != nil || diagnostics.MigrationFailed != 1 || diagnostics.MigrationCompleted != 1 || diagnostics.WorkspaceMembersTotal != 1 {
		t.Fatalf("failure must be reported while healthy source migrates: %#v, %v", diagnostics, err)
	}
	if err := os.WriteFile(manifestPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	app.closeSessionServices()
	app = NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	diagnostics, err = app.GetSessionArchitectureDiagnostics()
	if err != nil || diagnostics.MigrationFailed != 0 || diagnostics.MigrationCompleted != 2 || diagnostics.WorkspaceMembersTotal != 2 {
		t.Fatalf("repaired source must retry: %#v, %v", diagnostics, err)
	}
}

func TestCanonicalV4MigrationRejectsMissingWorkspaceBeforePublication(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	old := coldV4MigrationFixture(t, root, "published")
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	source := desktopMigrationSource{root: root, scope: "global"}
	err := app.migrateCanonicalSession(t.Context(), old, source, "missing-workspace", "published")
	if !errors.Is(err, workspacestate.ErrWorkspaceNotFound) {
		t.Fatalf("expected interruption at registry publication: %v", err)
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: "published"}
	if _, err := app.desktopSessionService("").Query().Snapshot(t.Context(), ref); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("invalid workspace must not publish content: %v", err)
	}
	app.closeSessionServices()
	app = NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := app.GetSessionArchitectureDiagnostics()
	if err != nil || diagnostics.MigrationFailed != 0 || diagnostics.MigrationCompleted != 1 || diagnostics.SessionHeadersTotal != 1 || diagnostics.WorkspaceMembersTotal != 1 {
		t.Fatalf("restart must attach existing target exactly once: %#v, %v", diagnostics, err)
	}
}

func TestCanonicalV4MigrationPublishesHeaderThenWorkspaceMembershipIdempotently(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "project", "sessions-v4")
	sourceService, err := session.NewService("source", session.NewFilesystemPersistence(sourceRoot))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := sourceService.Create(t.Context(), session.CreateOptions{SessionID: "legacy-canonical"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": map[string]any{"id": "user", "role": "user", "content": "migrate me"}})
	if _, err := runtime.Session().AppendBatch(t.Context(), "content", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := sourceService.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	source := desktopMigrationSource{
		root: sourceRoot, scope: "project", workspaceRoot: filepath.Join(root, "workspace"),
		exact: map[string]bool{"legacy-canonical": true},
	}
	if err := app.migrateCanonicalStore(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := app.migrateCanonicalStore(t.Context(), source); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	info, err := app.desktopSessionService("").Query().List(t.Context(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Sessions) != 1 || info.Sessions[0].SessionID != "legacy-canonical" || info.Sessions[0].Origin != session.SessionOriginCanonicalImport {
		t.Fatalf("migrated sessions = %#v", info.Sessions)
	}
	state, err := app.desktopSessions.workspaceState.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := desktopWorkspaceID("project", source.workspaceRoot)
	if got := state.Workspaces[workspaceID].SessionIDs; len(got) != 1 || got[0] != "legacy-canonical" {
		t.Fatalf("workspace sessions = %#v", got)
	}
}

func TestExactLegacyTabMigrationFreezesIntoHeaderBackedSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	legacyDir := filepath.Join(root, "sessions")
	legacyPath := filepath.Join(legacyDir, "open-tab.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "user", Role: provider.RoleUser, Content: "legacy content"})
	if err := legacy.Save(legacyPath); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	source := desktopMigrationSource{root: legacyDir, scope: "global", exact: map[string]bool{legacyPath: true}}
	if err := app.migrateLegacyDirectory(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	state, err := app.desktopSessions.workspaceState.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs
	if len(ids) != 1 {
		t.Fatalf("migrated ids = %#v", ids)
	}
	page, err := app.ReadSessionHistory(session.SessionRef{HostID: "local", SessionID: ids[0]}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[1].Content != "legacy content" {
		t.Fatalf("legacy history = %#v", page.Messages)
	}
	info, err := app.desktopSessionService("").Query().List(t.Context(), "", 10)
	if err != nil || len(info.Sessions) != 1 || info.Sessions[0].Origin != session.SessionOriginLegacyImport {
		t.Fatalf("legacy header list = %#v, err=%v", info.Sessions, err)
	}
}

func TestPendingCreateRecoveryAttachesDurableSessionAndDropsMissingReservation(t *testing.T) {
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", filepath.Join(root, "project"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pending := range []workspacestate.PendingCreate{
		{OperationID: "durable-op", WorkspaceID: workspaceID, SessionID: "durable"},
		{OperationID: "missing-op", WorkspaceID: workspaceID, SessionID: "missing"},
	} {
		if err := app.desktopSessions.workspaceState.BeginCreate(t.Context(), pending); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{
		SessionID: "durable", CWD: root, Origin: session.SessionOriginNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.recoverDesktopPendingCreates(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := app.desktopSessions.workspaceState.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Workspaces[workspaceID].SessionIDs; len(got) != 1 || got[0] != "durable" {
		t.Fatalf("workspace sessions = %#v", got)
	}
	if len(state.PendingCreates) != 0 {
		t.Fatalf("pending creates = %#v", state.PendingCreates)
	}
}

func TestStartupRecoveryDoesNotAbortNewInFlightCreate(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	root := t.TempDir()
	app.desktopSessions.root = filepath.Join(root, "sessions")
	store := workspacestate.NewStore(filepath.Join(root, "state.json"))
	app.desktopSessions.workspaceState = store
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	startup, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginCreate(t.Context(), workspacestate.PendingCreate{OperationID: "live", WorkspaceID: workspaceID, SessionID: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := app.recoverDesktopPendingCreateSnapshot(t.Context(), startup.PendingCreates); err != nil {
		t.Fatal(err)
	}
	after, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.PendingCreates["live"].OperationID != "live" {
		t.Fatal("startup replay removed the current create reservation")
	}
}

func TestCanonicalMigrationRemapsConflictingSessionIDDeterministically(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "old")
	sourceService, err := session.NewService("migration-source", session.NewFilesystemPersistence(sourceRoot))
	if err != nil {
		t.Fatal(err)
	}
	sourceRuntime, err := sourceService.Create(t.Context(), session.CreateOptions{SessionID: "same-id"})
	if err != nil {
		t.Fatal(err)
	}
	appendMessage := func(runtime *session.Runtime, id, content string) {
		payload, _ := json.Marshal(map[string]any{"message": map[string]any{"id": id, "role": "user", "content": content}})
		if _, err := runtime.Session().AppendBatch(t.Context(), id, []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Session().Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage(sourceRuntime, "source", "source content")
	if err := sourceService.Close(t.Context(), sourceRuntime.Ref()); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	targetRuntime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "same-id", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	appendMessage(targetRuntime, "target", "different target content")

	source := desktopMigrationSource{root: sourceRoot, scope: "global", exact: map[string]bool{"same-id": true}}
	if err := app.migrateCanonicalStore(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := app.migrateCanonicalStore(t.Context(), source); err != nil {
		t.Fatalf("repeat conflict migration: %v", err)
	}
	state, err := app.desktopSessions.workspaceState.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs
	if len(ids) != 1 || len(ids[0]) < len("migr-") || ids[0][:len("migr-")] != "migr-" {
		t.Fatalf("conflict ids = %#v", ids)
	}
	page, err := app.ReadSessionHistory(session.SessionRef{HostID: localDesktopHostID, SessionID: ids[0]}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Content != "source content" {
		t.Fatalf("remapped history = %#v", page.Messages)
	}
}
