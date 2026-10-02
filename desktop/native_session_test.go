package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestOpenHistoricalDAGPreservesSelectedHeadAcrossTabPersistence(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "native-heads.jsonl")
	loaded := agent.NewSession("system")
	loaded.Add(provider.Message{Role: provider.RoleUser, Content: "shared"})
	loaded.Add(provider.Message{Role: provider.RoleAssistant, Content: "original answer"})
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.ForkHead(path, loaded.Snapshot()[1].ID, agent.HeadKindFork, "alternate"); err != nil {
		t.Fatal(err)
	}
	loaded.Add(provider.Message{Role: provider.RoleAssistant, Content: "alternate answer"})
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(agent.SessionEventLogPath(path))
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	source := &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: agent.SessionMainHead}
	meta, err := app.OpenTopicSession("global", "", "", nativeSessionSourceRoute(source))
	if err != nil {
		t.Fatal(err)
	}
	tab := waitForTabReady(t, app, meta.ID)
	ctrl, ok := tab.Ctrl.(*control.Controller)
	if !ok || !ctrl.NativeLegacySession() {
		t.Fatal("DAG was converted")
	}
	history := ctrl.History()
	if history[len(history)-1].Content != "original answer" {
		t.Fatalf("wrong head: %+v", history)
	}
	after, _ := os.ReadFile(agent.SessionEventLogPath(path))
	if !bytes.Equal(before, after) {
		t.Fatal("opening changed the DAG")
	}
	body, err := json.Marshal(persistedDesktopTabEntry(tab))
	if err != nil {
		t.Fatal(err)
	}
	var entry desktopTabEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatal(err)
	}
	entry.historicalSource = source
	restored := &WorkspaceTab{}
	prepareRestoredTabIdentity(restored, entry)
	if restored.SessionHeadID != agent.SessionMainHead {
		t.Fatal("restart lost the selected head")
	}
}

func TestOpenHistoricalDirectoryUsesNativeStore(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	const id = "native-open"
	coldV4MigrationFixture(t, root, id)
	path := filepath.Join(root, id)
	app := NewApp()
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	source := &SessionSourceRef{HostID: localDesktopHostID, Path: path, SourceKey: desktopSourceKey(path, "")}
	meta, err := app.OpenTopicSession("global", "", "", nativeSessionSourceRoute(source))
	if err != nil {
		t.Fatal(err)
	}
	tab := waitForTabReady(t, app, meta.ID)
	if tab.SessionPath != path || tab.SessionID != "" {
		t.Fatalf("historical locator changed: %+v", meta)
	}
	ctrl, ok := tab.Ctrl.(*control.Controller)
	if !ok || ctrl.SessionService() == app.desktopSessionService("") {
		t.Fatal("opened a migrated runtime")
	}
	ref, ok := ctrl.SessionRef()
	if !ok || ref.SessionID != id {
		t.Fatalf("wrong native identity: %+v", ref)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.SourceMappings) != 0 || len(state.PendingOperations) != 0 {
		t.Fatal("opening started conversion")
	}
	if _, err := app.desktopSessionService("").Query().Snapshot(t.Context(), ref); err == nil {
		t.Fatal("opening duplicated the transcript")
	}
	if err := app.ensureTabControllerWorkspace(tab); err != nil {
		t.Fatal(err)
	}
	if tab.Ctrl != ctrl {
		t.Fatal("native directory identity caused a redundant rebuild")
	}
}

func TestNativeSessionNewRegistersCurrentStoreAndWorkspace(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "jsonl", true: "stored-directory"}[directory], func(t *testing.T) {
			isolateDesktopUserDirs(t)
			app := NewApp()
			current := app.desktopSessionService("")
			historical := current
			path := filepath.Join(t.TempDir(), "old.jsonl")
			var ref session.SessionRef
			if directory {
				root := t.TempDir()
				coldV4MigrationFixture(t, root, "old-directory")
				var err error
				historical, err = app.historicalSessionService(root)
				if err != nil {
					t.Fatal(err)
				}
				ref = session.SessionRef{HostID: localDesktopHostID, SessionID: "old-directory"}
				path = filepath.Join(root, ref.SessionID)
			} else if err := os.WriteFile(path, []byte("{\"role\":\"user\",\"content\":\"old\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			// The app's historical service must release its ownership file before
			// TempDir removes the source root on Windows.
			t.Cleanup(app.closeSessionServices)
			exec := agent.New(nil, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
			ctrl := control.New(control.Options{Runner: exec, Executor: exec, Sink: event.Discard,
				SessionService: historical, SessionCreateService: current, ExclusiveSession: directory, NativeLegacySession: !directory,
				OnSessionRotation: app.prepareDesktopSessionRotation})
			t.Cleanup(func() { ctrl.Close(); <-ctrl.Closed() })
			if directory {
				if _, err := ctrl.OpenSession(t.Context(), ref); err != nil {
					t.Fatal(err)
				}
			} else {
				loaded, err := agent.LoadSession(path)
				if err != nil {
					t.Fatal(err)
				}
				ctrl.Resume(loaded, path)
			}
			tab := &WorkspaceTab{ID: "native", Scope: "global", SessionPath: path, Ctrl: ctrl, Ready: true}
			app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
			app.activeTabID = tab.ID
			if err := ctrl.NewSession(); err != nil {
				t.Fatal(err)
			}
			created, ok := ctrl.SessionRef()
			if !ok || ctrl.SessionService() != current {
				t.Fatalf("new session retained historical store: %+v", created)
			}
			info, err := current.Query().Stat(context.Background(), created)
			if err != nil || info.Origin != session.SessionOriginNew {
				t.Fatalf("new metadata: %+v %v", info, err)
			}
			contained, err := app.workspaceRegistry().Contains(t.Context(), created.SessionID)
			if err != nil || !contained {
				t.Fatalf("new session missing workspace: %v", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("new session removed old source: %v", err)
			}
			if got := ctrl.History(); len(provider.ModelMessages(got)) > 1 {
				t.Fatalf("new session retained old history: %+v", got)
			}
		})
	}
}
