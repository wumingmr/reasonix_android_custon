package main

import (
	"slices"
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestWorkspacePlaceholderRuntimeIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	root := t.TempDir()
	nodes := a.projectTreeRuntimeTopics([]catalogRuntimeSnapshot{{
		tabID: "starting", scope: "project", workspaceRoot: root, topicID: "draft", open: true,
	}})
	if len(nodes) != 1 || !slices.Contains(nodes[0].Node.IdentityAliases, "topic\x00draft") {
		t.Fatalf("startup must identify its owned placeholder: %+v", nodes)
	}
}

func TestWorkspaceCanonicalPlaceholderAliasIsUnambiguous(t *testing.T) {
	state := workspacestate.State{
		Workspaces:   map[string]workspacestate.Workspace{"w": {ID: "w", SessionIDs: []string{"first"}}},
		Presentation: map[string]workspacestate.Presentation{"first": {TopicID: "draft"}, "branch": {TopicID: "draft"}},
	}
	if !slices.Contains(sourceAliases(state, "w", "first"), "topic\x00draft") {
		t.Fatal("the first durable session must retire its placeholder")
	}
	w := state.Workspaces["w"]
	w.SessionIDs = append(w.SessionIDs, "branch")
	state.Workspaces["w"] = w
	for _, id := range w.SessionIDs {
		if slices.Contains(sourceAliases(state, "w", id), "topic\x00draft") {
			t.Fatal("a shared topic is not a unique session alias")
		}
	}
}
