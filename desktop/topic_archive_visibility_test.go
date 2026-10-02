package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/sessioncatalog"
)

func TestLegacyTopicArchiveVisibility(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		for _, metadataOnly := range []bool{false, true} {
			for _, topicRoute := range []bool{true, false} {
				name := scope + "/materialized"
				if metadataOnly {
					name = scope + "/lazy"
				}
				if topicRoute {
					name += "/topic"
				} else {
					name += "/session"
				}
				t.Run(name, func(t *testing.T) {
					a, topic, root := topicRemovalFixture(t, scope, "Historical conversation")
					openCatalog := func(owner *App, filename string) {
						t.Helper()
						catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), filename), MetadataOnly: metadataOnly, DisableRepair: true})
						if err != nil {
							t.Fatal(err)
						}
						owner.sessionCatalog.Store(catalog)
						// Register after this generation's TempDir cleanup and bind
						// the owner now, so Windows sees closed SQLite handles first.
						t.Cleanup(func() {
							owner.desktopSessions.readSnapshots.close()
							if !owner.stopSessionCatalog(sessionCatalogTestDeadline) {
								t.Error("session catalog did not stop before directory cleanup")
							}
						})
					}
					if err := a.SetTopicPinned(topic.ID, true); err != nil {
						t.Fatal(err)
					}
					canonicalRoot := pinDesktopSessionRoot(t, a)
					sourceRoot := root
					if scope == "global" {
						sourceRoot = globalWorkspaceRoot()
					}
					dir := desktopSessionDir(sourceRoot)
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					sourcePath := writeTopicSessionWithPrompt(t, dir, "archive-visibility.jsonl", topic.ID, topic.Title, root, "preserve this history", time.Now())
					original, err := os.ReadFile(sourcePath)
					if err != nil {
						t.Fatal(err)
					}
					openCatalog(a, "catalog.sqlite")
					reconcileSessionCatalogForTest(t, a, dir, scope, root)
					assertVisible := func(want int) {
						t.Helper()
						page, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 50})
						if err != nil || len(page.Items) != want {
							t.Fatalf("sidebar = %+v, %v; want %d", page.Items, err, want)
						}
					}
					assertVisible(1)
					if topicRoute {
						result, err := a.RemoveTopic(inspectedTopicRemoval(t, a, topic.ID))
						if err != nil || !result.Committed {
							t.Fatalf("archive topic = %+v, %v", result, err)
						}
					} else {
						result, err := a.ArchiveSessionTarget(SessionSelector{SessionPath: sourcePath})
						if err != nil || !result.Committed {
							t.Fatalf("archive session = %+v, %v", result, err)
						}
					}
					assertVisible(0)
					trash, err := a.ListTrashEntries("", "", 50)
					if err != nil || len(trash.Items) != 1 || trash.Items[0].Ref == nil {
						t.Fatalf("trash = %+v, %v", trash, err)
					}
					if err := a.RestoreCanonicalSession(*trash.Items[0].Ref); err != nil {
						t.Fatal(err)
					}
					assertVisible(1)
					if err := a.ArchiveCanonicalSession(*trash.Items[0].Ref); err != nil {
						t.Fatal(err)
					}
					assertVisible(0)
					if err := a.PurgeCanonicalSession(*trash.Items[0].Ref); err != nil {
						t.Fatal(err)
					}
					assertVisible(0)
					retained, err := os.ReadFile(sourcePath)
					if err != nil || !bytes.Equal(retained, original) {
						t.Fatal("archive/purge changed the retained legacy source")
					}
					// A stale metadata-only row would take the native-confirmation
					// route and fail exactly like the reported second archive attempt.
					stale := inspectedTopicRemoval(t, a, topic.ID)
					stale.OperationID += "-stale"
					failed, err := a.RemoveTopic(stale)
					if err != nil || failed.Committed || failed.ErrorMessage != "session not found" {
						t.Fatalf("stale archive = %+v, %v", failed, err)
					}
					assertVisible(0)
					// The retained source and metadata must not recreate a conversation
					// after a fresh process loads the registry without a warm catalog.
					a.closeSessionServices()
					a.desktopSessions.readSnapshots.close()
					if !a.stopSessionCatalog(sessionCatalogTestDeadline) {
						t.Fatal("session catalog did not stop before the fresh process")
					}
					a = NewApp()
					a.ctx = t.Context()
					a.desktopSessions.root = canonicalRoot
					installNoopRuntimeEvents(a)
					t.Cleanup(a.closeSessionServices)
					assertVisible(0)
					state, err := a.workspaceRegistry().LoadProjection(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					pins, err := a.historicalPinnedShellsFromProjection(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root}, state, workspacestate.NewWorkspaceIndex(state), desktopProject{Root: root, Topics: []string{topic.ID}, PinnedTopics: []string{topic.ID}})
					if err != nil || len(pins) != 0 {
						t.Fatalf("purged topic returned in pinned shell: %+v, %v", pins, err)
					}
					// Independent legacy content can share the consumed topic ID.
					// Its real source row must remain visible after rediscovery.
					sibling := writeTopicSessionWithPrompt(t, dir, "independent-sibling.jsonl", topic.ID, topic.Title, root, "independent history", time.Now())
					openCatalog(a, "rebuilt-catalog.sqlite")
					reconcileSessionCatalogForTest(t, a, dir, scope, root)
					page, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 1})
					if err != nil || len(page.Items) != 1 || !sameDesktopPath(page.Items[0].SessionPath, sibling) || page.NextCursor != "" {
						t.Fatalf("topic adoption hid independent history: %+v, %v", page, err)
					}
					if _, err := a.CreateTopic(scope, root, "Unrelated empty topic"); err != nil {
						t.Fatal(err)
					}
					assertVisible(2)
				})
			}
		}
	}
}
