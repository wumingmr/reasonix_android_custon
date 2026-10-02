package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestTopicRemovalReadsSupportedV1Organization(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "")
	file, err := readTopicRemovalProjects()
	if err != nil {
		t.Fatal(err)
	}
	file.GlobalGroups = []desktopGroup{{ID: "saved", Title: "Saved", TopicIDs: []string{topic.ID}}}
	if err := saveProjectsFile(file); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(desktopConfigDir(), desktopProjectOrganizationFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var organization map[string]any
	if err := json.Unmarshal(body, &organization); err != nil {
		t.Fatal(err)
	}
	organization["version"] = 1
	body, err = json.Marshal(organization)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	view, err := a.InspectTopicRemoval(TopicRemovalTarget{TopicID: topic.ID})
	if err != nil || !view.Allowed || view.Disposition != "archive_placeholder" {
		t.Fatalf("supported legacy organization must remain recoverable: %+v %v", view, err)
	}
}

func TestTopicRemovalRecoveryRejectsReusedIDInAnotherWorkspace(t *testing.T) {
	for _, action := range []string{"retry", "restore"} {
		t.Run(action, func(t *testing.T) {
			a, topic, _ := topicRemovalFixture(t, "global", "Original")
			request := inspectedTopicRemoval(t, a, topic.ID)
			if action == "retry" {
				interruptTopicRemoval(t, a, "topic-removal-before-metadata", func() { _, _ = a.RemoveTopic(request) })
			} else if result, err := a.RemoveTopic(request); err != nil || !result.Committed {
				t.Fatalf("remove: %+v %v", result, err)
			}
			root := t.TempDir()
			if err := addProject(root, "Other owner"); err != nil {
				t.Fatal(err)
			}
			if err := updateProjectsFile(func(file *desktopProjectFile) (bool, error) {
				i := projectIndexByRoot(file.Projects, root)
				file.Projects[i].Topics = append(file.Projects[i].Topics, topic.ID)
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := setTopicTitle(root, topic.ID, "New owner"); err != nil {
				t.Fatal(err)
			}
			if action == "retry" {
				result, err := a.RemoveTopic(request)
				if err != nil || result.Committed || result.ErrorCode != "state_conflict" {
					t.Fatalf("retry changed a new owner: %+v %v", result, err)
				}
			} else if err := a.restoreRemovedTopic(request.OperationID, request.Target.WorkspaceID); err == nil {
				t.Fatal("restored over a different owner")
			}
			if !topicIndexedInRegistry("project", root, topic.ID) || loadTopicTitle(root, topic.ID) != "New owner" {
				t.Fatal("new owner's data changed")
			}
		})
	}
}

func TestTopicRemovalCanonicalConfirmationFencedAtArchiveAdmission(t *testing.T) {
	for _, phase := range []string{"topic-sessions-before-archive", "before-archive-commit"} {
		t.Run(phase, func(t *testing.T) { testTopicRemovalCanonicalConfirmation(t, phase) })
	}
}

func testTopicRemovalCanonicalConfirmation(t *testing.T, checkpoint string) {
	a, _, _ := topicRemovalFixture(t, "global", "")
	tab, err := a.EnsureBlankTab("global", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseTab(tab.ID); err != nil {
			t.Error(err)
		}
	})
	request := inspectedTopicRemoval(t, a, tab.TopicID)
	a.lifecycleCheckpointHook = func(phase string) {
		if phase != checkpoint {
			return
		}
		title := "Edited after confirmation"
		if err := a.workspaceRegistry().UpdatePresentation(t.Context(), []string{tab.SessionID}, &title, nil); err != nil {
			t.Fatal(err)
		}
	}
	result, err := a.RemoveTopic(request)
	a.lifecycleCheckpointHook = nil
	if err != nil || result.Committed || result.ErrorCode != "state_conflict" {
		t.Fatalf("archived changed confirmation: %+v %v", result, err)
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil || state.SessionStates[tab.SessionID].Lifecycle != workspacestate.Active {
		t.Fatalf("changed session not active: %v", err)
	}
	// The archive child is durable by this point. Generic startup replay must
	// enforce the same confirmation fence instead of bypassing the topic RPC.
	if err := a.recoverDesktopOperations(t.Context(), false); err == nil {
		t.Fatal("replay accepted stale confirmation")
	}
	state, err = a.workspaceRegistry().Load(t.Context())
	if err != nil || state.SessionStates[tab.SessionID].Lifecycle != workspacestate.Active {
		t.Fatalf("replay archived the changed session: %v", err)
	}
}

func TestTopicRemovalInterruptedRestoreSurvivesUnrelatedEdit(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "Restore this")
	request := inspectedTopicRemoval(t, a, topic.ID)
	if result, err := a.RemoveTopic(request); err != nil || !result.Committed {
		t.Fatalf("remove: %+v %v", result, err)
	}
	interruptTopicRemoval(t, a, "topic-restore-before-metadata", func() {
		_ = a.restoreRemovedTopic(request.OperationID, request.Target.WorkspaceID)
	})
	other, err := a.CreateTopic("global", "", "Unrelated new topic")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.restoreRemovedTopic(request.OperationID, request.Target.WorkspaceID); err != nil {
		t.Fatalf("unrelated edit permanently blocks interrupted restore: %v", err)
	}
	if loadTopicTitle("", topic.ID) != topic.Title || loadTopicTitle("", other.ID) != other.Title {
		t.Fatal("restore changed unrelated data")
	}
}
