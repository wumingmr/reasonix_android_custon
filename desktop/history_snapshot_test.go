package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/history"
	"reasonix/internal/historycatalog"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncatalog"
)

func TestLegacyHistorySearchSnapshotSurvivesUnrelatedIndexWrites(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "search.jsonl")
	source := agent.NewSession("")
	for i := range 405 {
		source.Add(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("snapshot marker %d", i)})
	}
	if err := source.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	t.Cleanup(app.closeSessionServices)
	installSessionCatalogForTest(t, app, dir, "global", "")
	if err := history.RebuildSharedCatalog(t.Context(), []historycatalog.Root{{Path: dir, Scope: "global", Source: "global"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.CloseSharedCatalog(context.Background()) })
	deadline := time.After(5 * time.Second)
	for history.SharedCatalog() == nil {
		select {
		case <-deadline:
			t.Fatal("history catalog did not open")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := history.SharedCatalog().ReconcileRoot(t.Context(), historycatalog.Root{Path: dir, Scope: "global", Source: "global"}); err != nil {
		t.Fatal(err)
	}
	req := HistorySearchRequest{Query: "snapshot marker", Scope: "global", Limit: 200}
	first := app.SearchHistoryContent(req)
	if first.ReadError != nil || len(first.Items) != 200 || first.NextCursor == "" {
		t.Fatalf("first=%+v", first)
	}
	targetFirst, err := app.SearchHistoryContentForTarget(SessionSelector{SessionPath: path}, "snapshot marker", "", 200)
	if err != nil || len(targetFirst.Items) != 200 {
		t.Fatalf("target first=%+v err=%v", targetFirst, err)
	}
	other := t.TempDir()
	otherPath := filepath.Join(other, "other.jsonl")
	unrelated := agent.NewSession("")
	unrelated.Add(provider.Message{Role: provider.RoleUser, Content: "snapshot snapshot marker marker"})
	if err := unrelated.SaveSnapshot(otherPath); err != nil {
		t.Fatal(err)
	}
	if err := history.SharedCatalog().ReconcileRoot(t.Context(), historycatalog.Root{Path: other, Scope: "project", WorkspaceRoot: other}); err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, hit := range first.Items {
		seen[hit.MessageIndex] = true
	}
	req.Cursor = first.NextCursor
	for req.Cursor != "" {
		page := app.SearchHistoryContent(req)
		if page.ReadError != nil || page.StaleCursor || page.SnapshotID != first.SnapshotID {
			t.Fatalf("continuation=%+v", page)
		}
		for _, hit := range page.Items {
			if seen[hit.MessageIndex] {
				t.Fatalf("duplicate %d", hit.MessageIndex)
			}
			seen[hit.MessageIndex] = true
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 405 {
		t.Fatalf("hits=%d", len(seen))
	}
	targetNext, err := app.SearchHistoryContentForTarget(SessionSelector{SessionPath: path}, "snapshot marker", targetFirst.NextCursor, 200)
	if err != nil || len(targetNext.Items) != 200 {
		t.Fatalf("target next=%+v err=%v", targetNext, err)
	}
	req.Cursor, req.Query = first.NextCursor, "other query"
	if page := app.SearchHistoryContent(req); !page.StaleCursor {
		t.Fatal("cross-query cursor accepted")
	}
	app.tabs["switch"] = &WorkspaceTab{ID: "switch", Scope: "global", SessionPath: path}
	app.activeTabID = "switch"
	if app.activeSessionPath(app.activeSessionDir()) != path {
		t.Fatal("fixture did not activate the requested session")
	}
	req.Query = "snapshot marker"
	if page := app.SearchHistoryContent(req); !page.StaleCursor {
		t.Fatal("search cursor crossed an active-session switch")
	}
	req.Cursor, req.Status = "", "current"
	current := app.SearchHistoryContent(req)
	if current.ReadError != nil || len(current.Items) != 200 || !current.Items[0].Current {
		t.Fatalf("current search after switch=%+v", current)
	}
	if page, err := app.SearchHistoryContentForTarget(SessionSelector{SessionPath: path}, "snapshot marker", targetFirst.NextCursor, 200); err != nil || page.StaleCursor || len(page.Items) != 200 {
		t.Fatalf("exact-target search must survive unrelated focus changes: page=%+v err=%v", page, err)
	}
	app.activeTabID = ""
	req.Cursor, req.Status = first.NextCursor, ""
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	req.Query = "snapshot marker"
	if page := app.SearchHistoryContent(req); !page.StaleCursor {
		t.Fatal("deleted source stayed readable")
	}
}

func TestHistorySessionSnapshotKeepsOrderAndFencesRemoval(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	t.Cleanup(app.closeSessionServices)
	installSessionCatalogForTest(t, app, dir, "global", "")
	catalog := app.sessionCatalog.Load()
	for i := range 405 {
		if err := catalog.UpsertSession(t.Context(), sessioncatalog.SessionRecord{Path: filepath.Join(dir, fmt.Sprintf("%03d.jsonl", i)), Directory: dir, Scope: "global", TopicID: fmt.Sprint(i), LastActivityAt: int64(i), Health: sessioncatalog.HealthOK, TurnsState: sessioncatalog.TurnsValid}); err != nil {
			t.Fatal(err)
		}
	}
	req := HistorySessionPageRequest{Scope: "global", Limit: 200}
	first := app.ListHistorySessions(req)
	if first.ReadError != nil || len(first.Items) != 200 || first.NextCursor == "" {
		t.Fatalf("first=%+v", first)
	}
	changed, ok, err := catalog.GetSession(t.Context(), filepath.Join(dir, "000.jsonl"))
	if err != nil || !ok {
		t.Fatal(err)
	}
	changed.LastActivityAt, changed.CustomTitle = 999, "Updated"
	if err := catalog.UpsertSession(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range first.Items {
		seen[row.Path] = true
	}
	req.Cursor = first.NextCursor
	for req.Cursor != "" {
		page := app.ListHistorySessions(req)
		if page.ReadError != nil || page.StaleCursor {
			t.Fatalf("page=%+v", page)
		}
		for _, row := range page.Items {
			if seen[row.Path] || row.Title == "Updated" {
				t.Fatalf("live order leaked: %+v", row)
			}
			seen[row.Path] = true
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 405 {
		t.Fatalf("rows=%d", len(seen))
	}
	for _, name := range []string{"000", "001", "000"} {
		path := filepath.Join(dir, name+".jsonl")
		app.tabs[name] = &WorkspaceTab{ID: name, Scope: "global", SessionPath: path, Ctrl: &snapshotSwitchController{activationStubController: activationStubController{sessionPath: path}, dir: dir}}
		app.activeTabID = name
		if app.activeSessionPath(app.activeSessionDir()) != path {
			t.Fatal("fixture did not activate the requested session")
		}
		if page := app.ListHistorySessions(HistorySessionPageRequest{Scope: "global", Cursor: first.NextCursor, Limit: 200}); !page.StaleCursor {
			t.Fatal("metadata cursor crossed an active-session switch")
		}
		current := app.ListHistorySessions(HistorySessionPageRequest{Scope: "global", Status: "current", Limit: 200})
		if current.ReadError != nil || len(current.Items) != 1 || current.Items[0].Path != path || !current.Items[0].Current {
			t.Fatalf("current metadata after A-B-A switch=%+v", current)
		}
	}
	app.activeTabID = ""
	fresh := app.ListHistorySessions(req)
	if len(fresh.Items) == 0 || fresh.Items[0].Title != "Updated" {
		t.Fatalf("refresh=%+v", fresh)
	}
	if err := catalog.RemoveSession(t.Context(), changed.Path, "test"); err != nil {
		t.Fatal(err)
	}
	req.Cursor = first.NextCursor
	if page := app.ListHistorySessions(req); !page.StaleCursor {
		t.Fatal("removed source was not fenced")
	}
}

type snapshotSwitchController struct {
	activationStubController
	dir string
}

func (c *snapshotSwitchController) SessionDir() string { return c.dir }

func TestHistoryContentSearchDefaultsCoverEveryToolResultOnce(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tools.jsonl")
	source := agent.NewSession("")
	source.Add(provider.Message{Role: provider.RoleUser, Content: "list the servers"})
	source.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
		{ID: "ok", Name: "mcp_list", Arguments: "{}"},
		{ID: "bad", Name: "mcp_list", Arguments: "{}"},
	}})
	source.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "ok", Name: "mcp_list", Content: "servers: alpha zulumarker"})
	source.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "bad", Name: "mcp_list", Content: "error: quebecmarker unreachable"})
	if err := source.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	t.Cleanup(app.closeSessionServices)
	installSessionCatalogForTest(t, app, dir, "global", "")
	root := historycatalog.Root{Path: dir, Scope: "global", Source: "global"}
	if err := history.RebuildSharedCatalog(t.Context(), []historycatalog.Root{root}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.CloseSharedCatalog(context.Background()) })
	deadline := time.After(5 * time.Second)
	for history.SharedCatalog() == nil {
		select {
		case <-deadline:
			t.Fatal("history catalog did not open")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := history.SharedCatalog().ReconcileRoot(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"zulumarker", "quebecmarker"} {
		page := app.SearchHistoryContent(HistorySearchRequest{Query: query, Scope: "global", Limit: 50})
		if page.ReadError != nil || len(page.Items) != 1 || page.Items[0].Kind != "tool_output" {
			t.Fatalf("query %q: want exactly one tool_output hit, got %+v", query, page)
		}
	}
}

func TestHistorySearchBuildReadsEachSessionOnceAndKeepsRankOrder(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	digests := map[string]string{}
	paths := []string{filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")}
	for _, path := range paths {
		source := agent.NewSession("")
		source.Add(provider.Message{Role: provider.RoleUser, Content: "first interleave"})
		source.Add(provider.Message{Role: provider.RoleUser, Content: "second interleave"})
		if err := source.SaveSnapshot(path); err != nil {
			t.Fatal(err)
		}
		_, state, _, err := agent.LoadSessionDisplayMessages(path)
		if err != nil {
			t.Fatal(err)
		}
		digests[path] = state.DigestHex
	}
	loads := 0
	original := loadHistorySearchMessages
	loadHistorySearchMessages = func(path string) ([]provider.Message, agent.PersistedState, bool, error) {
		loads++
		return original(path)
	}
	t.Cleanup(func() { loadHistorySearchMessages = original })
	app := NewApp()
	app.ctx = t.Context()
	t.Cleanup(app.closeSessionServices)
	store := &app.desktopSessions.readSnapshots
	candidates, out := &readSnapshot{}, &readSnapshot{}
	t.Cleanup(func() { store.dispose(candidates); store.dispose(out) })
	ranked := []historycatalog.Candidate{
		{SessionPath: paths[0], MessageIndex: 0, Kind: "user_text"},
		{SessionPath: paths[1], MessageIndex: 1, Kind: "user_text"},
		{SessionPath: paths[0], MessageIndex: 1, Kind: "user_text"},
		{SessionPath: paths[1], MessageIndex: 0, Kind: "user_text"},
	}
	for _, row := range ranked {
		row.ContentDigest = digests[row.SessionPath]
		if err := store.append(t.Context(), candidates, row); err != nil {
			t.Fatal(err)
		}
	}
	fence, err := app.newReadSourceFence(store, out)
	if err != nil {
		t.Fatal(err)
	}
	build := &historySearchSnapshotBuild{ctx: t.Context(), app: app, store: store, snapshot: out, request: HistorySearchRequest{Query: "interleave"}, fence: fence, terms: []string{"interleave"}, cutoff: time.Now()}
	if err := build.run(candidates); err != nil {
		t.Fatal(err)
	}
	var got []HistorySearchHit
	if err := out.walk(t.Context(), func(b []byte) error {
		var hit HistorySearchHit
		if err := json.Unmarshal(b, &hit); err != nil {
			return err
		}
		got = append(got, hit)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loads != len(paths) {
		t.Fatalf("session loads = %d, want %d (one per session)", loads, len(paths))
	}
	if len(got) != len(ranked) {
		t.Fatalf("hits = %+v", got)
	}
	for i, hit := range got {
		if hit.SessionPath != ranked[i].SessionPath || hit.MessageIndex != ranked[i].MessageIndex || hit.Snippet == "" {
			t.Fatalf("hit %d = %+v, want rank order %+v", i, hit, ranked[i])
		}
	}
}
