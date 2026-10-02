package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/store"
)

func TestNativeHistorySearchDAGUsesSelectedBranchAndPatches(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	dir := tabSessionDir(tab)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tab.SessionPath = filepath.Join(dir, "dag-search.jsonl")
	if err := os.WriteFile(tab.SessionPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	message := func(id, head, parent, text string) map[string]any {
		return map[string]any{"type": "message", "id": id, "head": head, "parent": parent, "msgs": []provider.Message{{ID: id, Role: provider.RoleUser, Content: text}}}
	}
	entries := []map[string]any{
		{"type": "log", "generation": 1},
		message("root", "main", "", "shared original"),
		message("main-only", "main", "root", "main needle"),
		{"type": "fork", "head": "main", "new_head": "fork", "from": "root"},
		message("fork-only", "fork", "root", "fork needle"),
		{"type": "patch", "target": "root", "msgs": []provider.Message{{ID: "root", Role: provider.RoleUser, Content: "patched needle"}}},
		{"type": "select", "head": "fork"},
	}
	var data []byte
	for _, entry := range entries {
		entry["schema_version"], entry["at"] = 2, "2026-01-08T10:00:00Z"
		body, _ := json.Marshal(entry)
		data = append(data, append(body, '\n')...)
	}
	if err := os.WriteFile(store.SessionEventLog(tab.SessionPath), data, 0600); err != nil {
		t.Fatal(err)
	}
	var previousCursor string
	for _, head := range []string{"fork", "main"} {
		tab.SessionHeadID = head
		handle, err := a.BeginSessionHistoryReadForTab(tab.ID)
		if err != nil {
			t.Fatal(err)
		}
		first := awaitNativeSearch(t, a, handle.ID, "needle", "", 1)
		if len(first.Hits) != 1 || first.Hits[0].Preview != head+" needle" || !first.HasMore {
			t.Fatalf("wrong branch: %+v", first)
		}
		last := awaitNativeSearch(t, a, handle.ID, "needle", first.NextCursor, 1)
		if len(last.Hits) != 1 || last.Hits[0].Preview != "patched needle" || last.HasMore {
			t.Fatalf("patch not reflected: %+v", last)
		}
		if got := awaitNativeSearch(t, a, handle.ID, "original", "", 10); len(got.Hits) != 0 {
			t.Fatal("superseded text remained searchable")
		}
		if previousCursor != "" {
			page, err := a.SearchSessionHistoryRead(handle.ID, "needle", previousCursor, 1)
			if err != nil || page.Status != "stale_cursor" || len(page.Hits) != 0 {
				t.Fatalf("cross-branch cursor accepted: %+v %v", page, err)
			}
		}
		previousCursor = first.NextCursor
		a.ReleaseSessionHistoryRead(handle.ID)
	}
	after, _ := os.ReadFile(store.SessionEventLog(tab.SessionPath))
	if string(after) != string(data) {
		t.Fatal("search modified selected head or authoritative DAG")
	}
}
