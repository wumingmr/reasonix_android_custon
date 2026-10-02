package main

import (
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"testing"
)

func TestIndependentLegacyHeadsAdoptOneWithoutHidingSibling(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := filepath.Join(config.SessionDir(), "independent-heads.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "question", Role: provider.RoleUser, Content: "question"})
	legacy.Add(provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ForkHead(path, legacy.Snapshot()[2].ID, agent.HeadKindFork, "child"); err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{ID: "child", Role: provider.RoleUser, Content: "child only"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	rows := expandSessionSourceRows(ProjectNode{Key: "source", Kind: "global_topic", TopicID: "same", SessionPath: path})
	if len(rows) != 2 || projectNodeSessionKey(rows[0]) == projectNodeSessionKey(rows[1]) {
		t.Fatalf("heads=%+v", rows)
	}
	// Display metadata stays lightweight and is transferred when the selected
	// source is explicitly prepared.
	if err := app.SetSessionPinned(SessionSelector{Source: rows[0].Source}, true); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.SourceMappings) != 0 {
		t.Fatalf("pinning converted historical content: %+v", state.SourceMappings)
	}
	app.historicalImports.mu.Lock()
	app.historicalImports.initialize(app.bootContext())
	app.historicalImports.sources[rows[0].Source.SourceKey] = historicalSource{path: path, format: "legacy", scope: "global", head: rows[0].Source.HeadID}
	app.historicalImports.views[rows[0].Source.SourceKey] = historicalImportViewFromSource(rows[0].Source.SourceKey, app.historicalImports.sources[rows[0].Source.SourceKey])
	app.historicalImports.mu.Unlock()
	if _, err := app.ImportHistoricalSession(rows[0].Source.SourceKey); err != nil {
		t.Fatal(err)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.SourceMappings[rows[0].Source.SourceKey]; !ok {
		t.Fatal("explicit preparation did not adopt the selected head")
	}
	if _, ok := state.SourceMappings[rows[1].Source.SourceKey]; ok {
		t.Fatal("sibling was adopted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("source transcript changed during adoption")
	}
	// Path-only navigation resolves the selected head through the published
	// source index. A sibling's completed adoption must not capture this head.
	if err := agent.SelectSessionHead(path, rows[0].Source.HeadID); err != nil {
		t.Fatal(err)
	}
	ref, found, err := app.legacyCanonicalRef(t.Context(), path)
	if err != nil || !found || ref.SessionID != state.SourceMappings[rows[0].Source.SourceKey].SessionID {
		t.Fatalf("selected adopted head: ref=%+v found=%v err=%v", ref, found, err)
	}
	if err := agent.SelectSessionHead(path, rows[1].Source.HeadID); err != nil {
		t.Fatal(err)
	}
	if ref, found, err := app.legacyCanonicalRef(t.Context(), path); err != nil || found {
		t.Fatalf("unadopted sibling was redirected: ref=%+v found=%v err=%v", ref, found, err)
	}
}
