package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestBootLegacyTabReusesMigratedIdentityAfterMetadataRefresh(t *testing.T) {
	for _, dag := range []bool{false, true} {
		name := "linear"
		if dag {
			name = "selected DAG head"
		}
		t.Run(name, func(t *testing.T) { testBootLegacyAdoption(t, dag) })
	}
}

func testBootLegacyAdoption(t *testing.T, dag bool) {
	isolateDesktopUserDirs(t)
	path := filepath.Join(config.SessionDir(), "boot # %20 中文.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	messageCount := 1
	if dag {
		legacy := agent.NewSession("system")
		legacy.Add(provider.Message{ID: "user", Role: provider.RoleUser, Content: "preserved history"})
		if err := legacy.Save(path); err != nil {
			t.Fatal(err)
		}
		messageCount = 2
	} else if err := os.WriteFile(path, []byte("{\"role\":\"user\",\"content\":\"preserved history\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var expected session.SessionRef
	for attempt := range 2 {
		app := NewApp()
		app.ctx = t.Context()
		t.Cleanup(app.closeSessionServices)
		if err := app.migrateLegacyDirectory(t.Context(), desktopMigrationSource{root: filepath.Dir(path), scope: "global", exact: map[string]bool{path: true}}); err != nil {
			t.Fatal(err)
		}
		ref, found, err := app.legacyCanonicalRef(t.Context(), path)
		if err != nil || !found {
			t.Fatalf("adoption: %v, %v", found, err)
		}
		if attempt == 0 {
			expected = ref
		} else if ref != expected {
			t.Fatalf("restart changed identity: %v -> %v", expected, ref)
		}
		// Catalog refresh changes an import sidecar without changing history.
		meta, _, err := agent.LoadBranchMeta(path)
		if err != nil {
			t.Fatal(err)
		}
		meta.TopicTitle = "refreshed display title"
		if err := agent.SaveBranchMeta(path, meta); err != nil {
			t.Fatal(err)
		}
		ctrl := control.New(control.Options{
			Executor:       agent.New(nil, nil, agent.NewSession("test"), agent.Options{}, event.Discard),
			SessionService: app.desktopSessionService(""), ExclusiveSession: true,
		})
		t.Cleanup(ctrl.Close)
		lookup := path
		if runtime.GOOS == "windows" {
			lookup = strings.ToLower(filepath.VolumeName(path)) + path[len(filepath.VolumeName(path)):]
		}
		got, _, err := app.bindTabCanonicalSession(t.Context(), ctrl, &config.Config{}, "global", "", "", lookup, "", false)
		if err != nil || got != expected {
			t.Fatalf("boot must open adopted identity: got=%v want=%v err=%v", got, expected, err)
		}
		page, err := app.ReadSessionHistory(got, "", 10)
		if err != nil || len(page.Messages) != messageCount || page.Messages[messageCount-1].Content != "preserved history" {
			t.Fatalf("history=%+v err=%v", page, err)
		}
		tab := &WorkspaceTab{ID: "adopted", Scope: "global", Ctrl: ctrl, SessionID: got.SessionID, Ready: true}
		tab.sink = &tabEventSink{tabID: tab.ID, app: app}
		app.tabs[tab.ID], app.tabOrder, app.activeTabID = tab, []string{tab.ID}, tab.ID
		installNoopRuntimeEvents(app, tab.sink)
		if _, err := app.continueLegacySessionForTranscript(tab, ctrl, lookup, 10, true, false); err != nil {
			t.Fatalf("transcript continuation: %v", err)
		}
		list, err := app.desktopSessionService("").Query().List(t.Context(), "", 10)
		if err != nil || len(list.Sessions) != 1 {
			t.Fatalf("duplicate import: %+v, %v", list, err)
		}
		if attempt == 1 && !dag {
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			got, _, err := app.bindTabCanonicalSession(t.Context(), ctrl, &config.Config{}, "global", "", "", lookup, "", false)
			if err != nil || got != expected {
				t.Fatalf("missing retained source replaced adoption: %v %v", got, err)
			}
			if err := os.WriteFile(path, append(original, []byte("{\"role\":\"user\",\"content\":\"changed\"}\n")...), 0600); err != nil {
				t.Fatal(err)
			}
			if got, _, err := app.bindTabCanonicalSession(t.Context(), ctrl, &config.Config{}, "global", "", "", lookup, "", false); err != nil || got != expected {
				t.Fatalf("retained source changes must not replace the adopted conversation: %v %v", got, err)
			}
			if ref, _ := ctrl.SessionRef(); ref != expected {
				t.Fatalf("failed bind replaced original: %v", ref)
			}
		}
		ctrl.Close()
		app.closeSessionServices()
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source lost: %v", err)
	}
}
