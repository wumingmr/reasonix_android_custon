package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/topicstate"
)

func topicRemovalFixture(t *testing.T, scope, title string) (*App, TopicMeta, string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	root := ""
	if scope == "project" {
		root = t.TempDir()
	}
	topic, err := a.CreateTopic(scope, root, title)
	if err != nil {
		t.Fatal(err)
	}
	return a, topic, root
}

func inspectedTopicRemoval(t *testing.T, a *App, topicID string) TopicRemovalRequest {
	t.Helper()
	view, err := a.InspectTopicRemoval(TopicRemovalTarget{TopicID: topicID})
	if err != nil || !view.Allowed {
		t.Fatalf("inspect: %+v %v", view, err)
	}
	return TopicRemovalRequest{OperationID: "remove-" + topicID, Target: view.Target, ExpectedToken: view.Token}
}

func TestTopicRemovalClassificationAndRestore(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		for _, kind := range []string{"default", "named", "manual-default", "pinned", "grouped", "unknown-source"} {
			t.Run(scope+"/"+kind, func(t *testing.T) {
				title := ""
				if kind == "named" {
					title = "My plan"
				}
				if kind == "manual-default" {
					title = defaultTopicTitle
				}
				a, topic, root := topicRemovalFixture(t, scope, title)
				if kind == "pinned" {
					if err := a.SetTopicPinned(topic.ID, true); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "grouped" {
					if err := updateProjectsFile(func(file *desktopProjectFile) (bool, error) {
						group := desktopGroup{ID: "group", Title: "Plans", TopicIDs: []string{topic.ID}}
						if scope == "global" {
							file.GlobalGroups = []desktopGroup{group}
						} else {
							file.Projects[projectIndexByRoot(file.Projects, root)].Groups = []desktopGroup{group}
						}
						return true, nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "unknown-source" {
					if err := desktopTopicState.withExclusiveScope(root, func(ctx context.Context, store *topicstate.Store) error {
						_, err := store.Update(ctx, topic.ID, func(r *topicstate.Record) { r.TitleSource = "" })
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				req := inspectedTopicRemoval(t, a, topic.ID)
				out, err := a.RemoveTopic(req)
				if err != nil || !out.Committed {
					t.Fatalf("remove: %+v %v", out, err)
				}
				if topicIndexedInRegistry(scope, root, topic.ID) || loadTopicTitle(root, topic.ID) != "" {
					t.Fatal("removed topic still present")
				}
				page, err := a.ListTrashEntries("", "", 50)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "default" {
					if len(page.Items) != 0 || out.Disposition != "discard_placeholder" {
						t.Fatalf("default polluted trash: %+v %+v", out, page)
					}
					return
				}
				if len(page.Items) != 1 || !page.Items[0].CanRestore || page.Items[0].CanPreview {
					t.Fatalf("metadata trash: %+v", page)
				}
				row := page.Items[0]
				result, err := a.ApplySessionLifecycle(SessionLifecycleRequest{OperationID: "restore", Action: "restore", ExpectedGeneration: page.Generation, Targets: []SessionLifecycleTarget{{WorkspaceID: row.WorkspaceID, RecoveryEntryID: row.RecoveryEntryID}}})
				if err != nil || !result.Committed {
					t.Fatalf("restore: %+v %v", result, err)
				}
				if !topicIndexedInRegistry(scope, root, topic.ID) || loadTopicTitle(root, topic.ID) != topic.Title {
					t.Fatal("restore lost metadata")
				}
				waitForInitialCatalogReconcile(t, a)
				t.Cleanup(func() { a.stopSessionCatalog(time.Second) })
				visible, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 50})
				if err != nil || len(visible.Items) != 1 || visible.Items[0].TopicID != topic.ID {
					t.Fatalf("restored placeholder missing from completed catalog projection: %+v %v", visible, err)
				}
				if kind == "pinned" || kind == "grouped" {
					state, _ := a.workspaceRegistry().Load(t.Context())
					file, _ := readTopicRemovalProjects()
					item, err := topicRemovalCandidate(state, file, req.Target)
					if err != nil || (kind == "pinned" && !item.Topic.Pinned) || (kind == "grouped" && item.Topic.GroupID != "group") {
						t.Fatalf("organization not restored: %+v %v", item, err)
					}
				}
				// A delayed duplicate removal cannot remove the restored topic again.
				if out, err := a.RemoveTopic(req); err != nil || !out.Committed || !topicIndexedInRegistry(scope, root, topic.ID) {
					t.Fatalf("duplicate removal replayed: %+v %v", out, err)
				}
			})
		}
	}
}

func TestTopicRemovalRejectsChangedConfirmation(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "")
	req := inspectedTopicRemoval(t, a, topic.ID)
	if err := a.RenameTopic(topic.ID, "Keep this"); err != nil {
		t.Fatal(err)
	}
	out, err := a.RemoveTopic(req)
	if err != nil || out.Committed || out.ErrorCode != "state_conflict" {
		t.Fatalf("stale confirmation: %+v %v", out, err)
	}
	if loadTopicTitle("", topic.ID) != "Keep this" {
		t.Fatal("removed changed topic")
	}
}

func interruptTopicRemoval(t *testing.T, a *App, phase string, work func()) {
	t.Helper()
	a.lifecycleCheckpointHook = func(at string) {
		if at == phase {
			panic("simulated process exit")
		}
	}
	defer func() {
		a.lifecycleCheckpointHook = nil
		if recover() != "simulated process exit" {
			t.Fatal("checkpoint was not reached")
		}
	}()
	work()
}

func TestTopicRemovalRecoversInterruptedWrites(t *testing.T) {
	for _, phase := range []string{"topic-removal-before-index", "topic-removal-before-metadata", "topic-removal-before-commit"} {
		t.Run(phase, func(t *testing.T) {
			a, topic, _ := topicRemovalFixture(t, "global", "Keep metadata")
			req := inspectedTopicRemoval(t, a, topic.ID)
			interruptTopicRemoval(t, a, phase, func() { _, _ = a.RemoveTopic(req) })
			restarted := NewApp()
			restarted.ctx = t.Context()
			installNoopRuntimeEvents(restarted)
			t.Cleanup(restarted.closeSessionServices)
			state, err := restarted.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.reconcileTopicRemovals(state); err != nil {
				t.Fatal(err)
			}
			page, err := restarted.ListTrashEntries("", "", 50)
			if err != nil || len(page.Items) != 1 || !page.Items[0].CanRestore || topicIndexedInRegistry("global", "", topic.ID) {
				t.Fatalf("recovery: %+v %v", page, err)
			}
		})
	}
}

func TestTopicRemovalRecoveryDoesNotEraseNewerMetadata(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "Keep metadata")
	req := inspectedTopicRemoval(t, a, topic.ID)
	interruptTopicRemoval(t, a, "topic-removal-before-index", func() { _, _ = a.RemoveTopic(req) })
	if err := a.RenameTopic(topic.ID, "Newer title"); err != nil {
		t.Fatal(err)
	}
	state, _ := a.workspaceRegistry().Load(t.Context())
	if err := a.reconcileTopicRemovals(state); err == nil {
		t.Fatal("unfenced replay")
	}
	if loadTopicTitle("", topic.ID) != "Newer title" {
		t.Fatal("new title erased")
	}
}

func TestTopicRemovalRestoreInterruption(t *testing.T) {
	for _, phase := range []string{"topic-restore-before-metadata", "topic-restore-before-index", "topic-restore-before-commit"} {
		t.Run(phase, func(t *testing.T) {
			a, topic, _ := topicRemovalFixture(t, "global", "Restore me")
			req := inspectedTopicRemoval(t, a, topic.ID)
			if out, err := a.RemoveTopic(req); err != nil || !out.Committed {
				t.Fatalf("remove: %+v %v", out, err)
			}
			interruptTopicRemoval(t, a, phase, func() { _ = a.restoreRemovedTopic(req.OperationID, req.Target.WorkspaceID) })
			if err := a.restoreRemovedTopic(req.OperationID, req.Target.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if loadTopicTitle("", topic.ID) != topic.Title || !topicIndexedInRegistry("global", "", topic.ID) {
				t.Fatal("restore did not finish")
			}
		})
	}
}

func TestTopicRemovalPurgeDoesNotTouchRestoredOrRecreatedTopic(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "Archive me")
	req := inspectedTopicRemoval(t, a, topic.ID)
	if out, err := a.RemoveTopic(req); err != nil || !out.Committed {
		t.Fatalf("remove: %+v %v", out, err)
	}
	if err := createTopicState("", topic.ID, "New occupant", topicTitleSourceManual, 123); err != nil {
		t.Fatal(err)
	}
	if err := a.restoreRemovedTopic(req.OperationID, req.Target.WorkspaceID); !errors.Is(err, workspacestate.ErrMutationConflict) {
		t.Fatalf("must not overwrite: %v", err)
	}
	if err := a.purgeRemovedTopic(req.OperationID, req.Target.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if loadTopicTitle("", topic.ID) != "New occupant" {
		t.Fatal("purge touched new occupant")
	}
}

func TestTopicRemovalCorruptOrganizationIsNotEmpty(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "")
	if err := os.WriteFile(filepath.Join(desktopConfigDir(), desktopProjectOrganizationFile), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InspectTopicRemoval(TopicRemovalTarget{TopicID: topic.ID}); err == nil {
		t.Fatal("corruption treated as no organization")
	}
}

func TestTopicRemovalCanonicalBlankRetainsArchiveSemantics(t *testing.T) {
	a, _, _ := topicRemovalFixture(t, "global", "")
	tab, err := a.EnsureBlankTab("global", "")
	if err != nil {
		t.Fatal(err)
	}
	req := inspectedTopicRemoval(t, a, tab.TopicID)
	out, err := a.RemoveTopic(req)
	if err != nil || !out.Committed || out.Disposition != "archive_sessions" {
		t.Fatalf("canonical removal: %+v %v", out, err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].Ref == nil || page.Items[0].Ref.SessionID != tab.SessionID || !page.Items[0].CanRestore {
		t.Fatalf("canonical blank must be restorable: %+v %v", page, err)
	}
	if out, err := a.RemoveTopic(req); err != nil || !out.Committed {
		t.Fatalf("duplicate: %+v %v", out, err)
	}
}

func TestTopicRemovalWriteFailureReturnsIncompleteAndRetriesSameIntent(t *testing.T) {
	for _, phase := range []string{"topic-removal-before-index", "topic-removal-before-commit"} {
		t.Run(phase, func(t *testing.T) {
			a, topic, _ := topicRemovalFixture(t, "global", "Preserve on failure")
			req := inspectedTopicRemoval(t, a, topic.ID)
			path := filepath.Join(desktopConfigDir(), desktopProjectsFile)
			if phase == "topic-removal-before-commit" {
				path = a.workspaceRegistry().Path()
			}
			a.lifecycleCheckpointHook = func(at string) {
				if at != phase {
					return
				}
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			out, err := a.RemoveTopic(req)
			a.lifecycleCheckpointHook = nil
			if err != nil || out.Committed || out.ErrorCode != "operation_failed" {
				t.Fatalf("write failure reported success: %+v %v", out, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".saved", path); err != nil {
				t.Fatal(err)
			}
			out, err = a.RemoveTopic(req)
			if err != nil || !out.Committed {
				t.Fatalf("same intent retry: %+v %v", out, err)
			}
			page, err := a.ListTrashEntries("", "", 50)
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("retry duplicated/lost archive: %+v %v", page, err)
			}
		})
	}
}

func TestTopicRemovalWorkspaceUnavailableAndRestoreGroupMissing(t *testing.T) {
	a, topic, root := topicRemovalFixture(t, "project", "Project plan")
	if err := updateProjectsFile(func(file *desktopProjectFile) (bool, error) {
		file.Projects[projectIndexByRoot(file.Projects, root)].Groups = []desktopGroup{{ID: "old-group", Title: "Old", TopicIDs: []string{topic.ID}}}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	req := inspectedTopicRemoval(t, a, topic.ID)
	if out, err := a.RemoveTopic(req); err != nil || !out.Committed {
		t.Fatalf("remove: %+v %v", out, err)
	}
	if err := os.Rename(root, root+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err := a.restoreRemovedTopic(req.OperationID, req.Target.WorkspaceID); err == nil {
		t.Fatal("restored into missing workspace")
	}
	if err := os.Rename(root+"-offline", root); err != nil {
		t.Fatal(err)
	}
	if err := updateProjectsFile(func(file *desktopProjectFile) (bool, error) {
		file.Projects[projectIndexByRoot(file.Projects, root)].Groups = nil
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.restoreRemovedTopic(req.OperationID, req.Target.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	file := loadProjectsFile()
	project := file.Projects[projectIndexByRoot(file.Projects, root)]
	if len(project.Groups) != 0 || !containsDesktopString(project.Topics, topic.ID) {
		t.Fatal("missing group recreated or topic not restored")
	}
}

func TestTopicRemovalLegacyContentUsesCanonicalArchive(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "Historical content")
	dir := desktopSessionDir(globalWorkspaceRoot())
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeTopicSessionWithPrompt(t, dir, "removal-legacy.jsonl", topic.ID, topic.Title, globalWorkspaceRoot(), "keep this message", time.Now())
	// A real old source can outlive its desktop-projects index entry.
	if err := updateProjectsFile(func(file *desktopProjectFile) (bool, error) {
		file.GlobalTopics = removeString(file.GlobalTopics, topic.ID)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	req := inspectedTopicRemoval(t, a, topic.ID)
	out, err := a.RemoveTopic(req)
	if err != nil || !out.Committed || out.Disposition != "archive_sessions" {
		again, inspectErr := a.InspectTopicRemoval(req.Target)
		t.Fatalf("legacy content removal: %+v %v request=%+v inspection=%+v %v", out, err, req, again, inspectErr)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 || !page.Items[0].CanRestore || page.Items[0].Ref == nil {
		t.Fatalf("legacy content not archived: %+v %v", page, err)
	}
}

func TestTopicRemovalRejectsAmbiguousLegacyOwner(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "Keep both")
	root := t.TempDir()
	if err := addProject(root, "Other"); err != nil {
		t.Fatal(err)
	}
	if err := updateProjectsFile(func(file *desktopProjectFile) (bool, error) {
		i := projectIndexByRoot(file.Projects, root)
		file.Projects[i].Topics = append(file.Projects[i].Topics, topic.ID)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InspectTopicRemoval(TopicRemovalTarget{TopicID: topic.ID}); err == nil {
		t.Fatal("ambiguous inspection accepted")
	}
	if err := a.TrashTopic(topic.ID); err == nil {
		t.Fatal("legacy adapter removed ambiguous topic")
	}
	if !topicIndexedInRegistry("global", "", topic.ID) || !topicIndexedInRegistry("project", root, topic.ID) {
		t.Fatal("ambiguous owners changed")
	}
}
