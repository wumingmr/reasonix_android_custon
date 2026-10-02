package main

import (
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"testing"
)

func TestOpenTopicTabKeepsRunningParentInsteadOfCoveringLeaf(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := desktopSessionDir(globalWorkspaceRoot())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	q := provider.Message{Role: provider.RoleUser, Content: "question"}
	a := provider.Message{Role: provider.RoleAssistant, Content: "answer"}
	save := func(path, topic string, messages ...provider.Message) {
		t.Helper()
		session := agent.NewSession("sys")
		for _, message := range messages {
			session.Add(message)
		}
		if err := session.Save(path); err != nil {
			t.Fatal(err)
		}
		if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
			ID: agent.BranchID(path), Scope: "global", TopicID: topic, TopicTitle: "Upgraded",
		}); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(dir, "root.jsonl")
	leaf := filepath.Join(dir, "leaf.jsonl")
	save(root, "conversation", q, a)
	save(leaf, "legacy-leaf-topic", q, a,
		provider.Message{Role: provider.RoleUser, Content: "next"},
		provider.Message{Role: provider.RoleAssistant, Content: "done"})
	if err := agent.SaveBranchMetaPreserveUpdated(leaf, agent.BranchMeta{
		ID: "leaf", Scope: "global", TopicID: "legacy-leaf-topic",
		Recovered: true, ParentID: "root", RecoveryDepth: 1,
	}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	installSessionCatalogForTest(t, app, dir, "global", "")
	running := &WorkspaceTab{
		ID: "running", Scope: "global", TopicID: "conversation", SessionPath: root,
		Ctrl: &retargetRuntimeController{status: control.RuntimeStatus{Running: true}, path: root},
	}
	app.tabs = map[string]*WorkspaceTab{"running": running}

	_, resolved := app.resolveOpenTopicSessionPath("global", "", root)
	if resolved != root {
		t.Fatalf("resolve running open = %q, want parent %q", resolved, root)
	}

	meta, err := app.openTopicTab("global", "", "conversation", root)
	if err != nil {
		t.Fatal(err)
	}
	if running.SessionPath != root {
		t.Fatalf("running tab path = %q, want parent %q", running.SessionPath, root)
	}
	if meta.ID != "running" || meta.SessionPath != root {
		t.Fatalf("open meta = %+v, want focused running parent", meta)
	}

	idle := &WorkspaceTab{ID: "idle", Scope: "global", TopicID: "conversation", SessionPath: root}
	app.tabs = map[string]*WorkspaceTab{"idle": idle}
	_, idleResolved := app.resolveOpenTopicSessionPath("global", "", root)
	if idleResolved != leaf {
		t.Fatalf("resolve idle open = %q, want covering leaf %q", idleResolved, leaf)
	}
}
