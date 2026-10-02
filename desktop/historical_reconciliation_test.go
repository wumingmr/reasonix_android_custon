package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

func rewriteHistoricalRegistry(t *testing.T, app *App, change func(*workspacestate.State)) {
	t.Helper()
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	change(&state)
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.workspaceRegistry().Path(), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalDiscoveryRepairsAdoptionAndKeepsOneLifecycle(t *testing.T) {
	for _, damage := range []string{"mapping", "aliases", "target", "deleted"} {
		t.Run(damage, func(t *testing.T) {
			app, selector, _ := historicalArchiveFixture(t)
			result, err := app.ImportHistoricalSession(selector.Source.SourceKey)
			if err != nil {
				t.Fatal(err)
			}
			if damage == "mapping" || damage == "aliases" {
				appendMigrationTestMessage(t, app.desktopSessionService(""), result.Session, "continued new conversation")
			}
			title, pinned := "My retained title", true
			if err := app.workspaceRegistry().UpdatePresentation(t.Context(), []string{result.Session.SessionID}, &title, &pinned); err != nil {
				t.Fatal(err)
			}
			before, err := app.desktopSessionService("").Query().Snapshot(t.Context(), result.Session)
			if err != nil {
				t.Fatal(err)
			}
			if damage == "deleted" {
				if err := app.ArchiveCanonicalSession(result.Session); err != nil {
					t.Fatal(err)
				}
				// Simulate a previous build that retained its source after purge.
				state, err := app.workspaceRegistry().Load(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if err := app.workspaceRegistry().BeginPurge(t.Context(), result.Session.SessionID, state.Generation); err != nil {
					t.Fatal(err)
				}
			}
			app.stopHistoricalImports()
			app.closeSessionServices()
			if damage == "target" {
				if err := os.Rename(filepath.Join(app.desktopSessions.root, result.Session.SessionID), filepath.Join(t.TempDir(), "removed-target")); err != nil {
					t.Fatal(err)
				}
			} else {
				rewriteHistoricalRegistry(t, app, func(state *workspacestate.State) {
					mapping := state.SourceMappings[selector.Source.SourceKey]
					state.SourceMappings = map[string]workspacestate.SourceMapping{}
					if damage == "aliases" {
						mapping.SourceKey = "previous-key"
						state.SourceMappings[mapping.SourceKey] = mapping
						mapping.SourceKey, mapping.SessionID = "retired-key", "retired-copy"
						state.SourceMappings[mapping.SourceKey] = mapping
						retired := state.SessionStates[result.Session.SessionID]
						retired.Lifecycle = workspacestate.Deleted
						state.SessionStates[mapping.SessionID] = retired
					}
					for id, op := range state.PendingOperations {
						if op.Kind == "import" {
							delete(state.PendingOperations, id)
						}
					}
				})
			}
			app = newHistoricalLifecycleApp(t)
			if _, err := app.ListHistoricalSessions(); err != nil {
				t.Fatal(err)
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			mapping, found, err := state.ResolveSource(selector.Source.SourceKey)
			if err != nil || !found || mapping.SessionID != result.Session.SessionID {
				t.Fatalf("adoption not repaired: %+v %v %v", mapping, found, err)
			}
			if p := state.Presentation[result.Session.SessionID]; p.Title != title || !p.Pinned {
				t.Fatalf("presentation overwritten: %+v", p)
			}
			page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			if damage == "deleted" {
				if len(page.Items) != 0 {
					t.Fatalf("deleted source resurrected: %+v", page.Items)
				}
				return
			}
			if len(page.Items) != 1 || page.Items[0].Session == nil || *page.Items[0].Session != result.Session {
				t.Fatalf("expected only b: %+v", page.Items)
			}
			after, err := app.desktopSessionService("").Query().Snapshot(t.Context(), result.Session)
			if err != nil {
				t.Fatal(err)
			}
			if (damage == "mapping" || damage == "aliases") && !reflect.DeepEqual(before, after) {
				t.Fatal("receipt recovery overwrote continued target content")
			}
			if err := app.ArchiveCanonicalSession(result.Session); err != nil {
				t.Fatal(err)
			}
			if err := app.PurgeCanonicalSession(result.Session); err != nil {
				t.Fatal(err)
			}
			app.stopHistoricalImports()
			app.closeSessionServices()
			app = newHistoricalLifecycleApp(t)
			if _, err := app.ListHistoricalSessions(); err != nil {
				t.Fatal(err)
			}
			page, err = app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("purged source returned: %+v %v", page.Items, err)
			}
			if _, err := os.Stat(selector.Source.Path); !os.IsNotExist(err) {
				t.Fatalf("exclusive source was not purged: %v", err)
			}
		})
	}
}

func TestHistoricalDiscoveryHidesUnrecoverableSource(t *testing.T) {
	app, selector, _ := historicalArchiveFixture(t)
	if err := os.WriteFile(filepath.Join(selector.Source.Path, "manifest.json"), []byte("broken manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("unrecoverable source shown: %+v %v", page.Items, err)
	}
	if _, err := os.Stat(selector.Source.Path); err != nil {
		t.Fatal("hiding destroyed the source", err)
	}
}

func TestCanonicalSidebarOmitsMissingMember(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := newHistoricalLifecycleApp(t)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspace, "missing", ""); err != nil {
		t.Fatal(err)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("missing member shown: %+v %v", page.Items, err)
	}
	if _, err := app.desktopSessionService("").Query().Stat(t.Context(), session.SessionRef{HostID: localDesktopHostID, SessionID: "missing"}); err == nil {
		t.Fatal("created an empty replacement")
	}
}
