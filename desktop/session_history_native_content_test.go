package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

func TestCompatibilityColdContentUsesReadOnlyNativePreparation(t *testing.T) {
	for _, kind := range []string{"checkpoint", "schema1"} {
		t.Run(kind, func(t *testing.T) {
			a := historySliceTestApp(t)
			t.Cleanup(a.closeHistoryReaders)
			tab := newColdHistoryTab(t, a)
			// Production global tabs retain the current workspace root even
			// when opening a source in the pre-workspace global directory.
			tab.WorkspaceRoot = globalWorkspaceRoot()
			dir := config.SessionDir()
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			tab.SessionPath = filepath.Join(dir, kind+".jsonl")
			answer := strings.Repeat("完整正文🧭", 25000)
			messages := []provider.Message{historySliceUser(0, "question"), historySliceAssistant(0, answer)}
			var checkpoint []byte
			for _, message := range messages {
				body, _ := json.Marshal(message)
				checkpoint = append(checkpoint, append(body, '\n')...)
			}
			var event []byte
			if kind == "schema1" {
				checkpoint = []byte("{\"role\":\"user\",\"content\":\"obsolete\"}\n")
				event, _ = json.Marshal(map[string]any{"schema_version": 1, "type": "replace", "messages": messages})
				if err := os.WriteFile(store.SessionEventLog(tab.SessionPath), event, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(tab.SessionPath, checkpoint, 0600); err != nil {
				t.Fatal(err)
			}
			page := a.HistorySliceForTab(tab.ID, HistorySliceRequest{Entries: 2})
			if page.Error != "" || len(page.Entries) != 2 || len(page.Entries[1].Refs) == 0 {
				t.Fatalf("compatibility slice: %+v", page)
			}
			ref := page.Entries[1].Refs[0]
			if ref.ReadHandleID != "" {
				t.Fatal("fixture did not exercise the unbound compatibility RPC")
			}
			var full strings.Builder
			for i := 0; ; i++ {
				chunk := a.HistoryContentForTab(tab.ID, ref, i)
				if chunk.Stale {
					t.Fatalf("compatibility content became stale at %d", i)
				}
				full.WriteString(chunk.Data)
				if chunk.Done {
					break
				}
			}
			if full.String() != answer {
				t.Fatal("compatibility content was truncated or used an obsolete checkpoint")
			}
			chunk, err := a.HistoryContentForTarget(SessionSelector{SessionPath: tab.SessionPath}, ref, 0)
			if err != nil || chunk.Stale || chunk.Data == "" || !strings.HasPrefix(answer, chunk.Data) {
				t.Fatalf("explicit compatibility target: %+v %v", chunk, err)
			}
			after, _ := os.ReadFile(tab.SessionPath)
			afterEvent, _ := os.ReadFile(store.SessionEventLog(tab.SessionPath))
			if string(after) != string(checkpoint) || string(afterEvent) != string(event) {
				t.Fatal("unbound content repaired authoritative storage")
			}
			if _, err := os.Stat(store.SessionDisplayIndex(tab.SessionPath)); !os.IsNotExist(err) {
				t.Fatal("unbound content wrote a compatibility display sidecar")
			}
		})
	}
}

func TestCompatibilityNativeBorrowCancellationKeepsOtherReader(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	dir := tabSessionDir(tab)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tab.SessionPath = filepath.Join(dir, "shared.jsonl")
	if err := os.WriteFile(tab.SessionPath, []byte("{\"role\":\"user\",\"content\":\"question\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	handle, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.ReleaseSessionHistoryRead(handle.ID)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = a.withNativeHistoryPager(ctx, tab.SessionPath, "", func(ctx context.Context, pager *agent.DisplayPager, _ string) error {
		cancel()
		_, err := pager.Entries(0, 1)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled compatibility reader continued: %v", err)
	}
	page, err := a.ReadSessionHistorySlice(handle.ID, HistorySliceRequest{Entries: 1})
	if err != nil || page.Status != "ready" || len(page.Page.Entries) != 1 {
		t.Fatalf("borrower cancelled its peer: %+v %v", page, err)
	}
}

func TestNativeHistorySchemaOneColdContentStaysOnBinding(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	dir := tabSessionDir(tab)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tab.SessionPath = filepath.Join(dir, "schema1.jsonl")
	checkpoint := []byte("{\"role\":\"user\",\"content\":\"obsolete checkpoint\"}\n")
	if err := os.WriteFile(tab.SessionPath, checkpoint, 0600); err != nil {
		t.Fatal(err)
	}
	answer := strings.Repeat("原始内容🧭", 60000)
	event, err := json.Marshal(map[string]any{"schema_version": 1, "type": "replace", "messages": []provider.Message{historySliceUser(0, "event question"), historySliceAssistant(0, answer)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.SessionEventLog(tab.SessionPath), event, 0600); err != nil {
		t.Fatal(err)
	}
	handle, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := a.ReadSessionHistorySlice(handle.ID, HistorySliceRequest{Entries: 2})
	if err != nil || page.Status != "ready" || page.Page.Source != "event-log" || len(page.Page.Entries) != 2 {
		t.Fatalf("cold event slice: %+v %v", page, err)
	}
	outline, err := a.ReadSessionHistoryOutline(handle.ID, session.HistoryOutlineRequest{})
	if err != nil || outline.Status != "ready" || len(outline.Entries) != 1 || outline.Entries[0].Prompt != "event question" {
		t.Fatalf("cold event outline: %+v %v", outline, err)
	}
	var ref HistoryContentRef
	for _, entry := range page.Page.Entries {
		for _, candidate := range entry.Refs {
			if candidate.Field == "content" {
				ref = candidate
			}
		}
	}
	if ref.ReadHandleID != handle.ID {
		t.Fatalf("content lost its read owner: %+v", ref)
	}
	var full strings.Builder
	for i := 0; ; i++ {
		chunk := a.HistoryContentForTab(tab.ID, ref, i)
		if chunk.Stale {
			t.Fatalf("bound content went stale at chunk %d", i)
		}
		full.WriteString(chunk.Data)
		if chunk.Done {
			break
		}
	}
	if full.String() != answer {
		t.Fatal("cold event content was truncated or read from the obsolete checkpoint")
	}
	if chunk := a.HistoryContentForTab("other-tab", ref, 0); !chunk.Stale || chunk.Data != "" {
		t.Fatal("another navigation accepted this content ref")
	}
	if chunk, err := a.HistoryContentForTarget(SessionSelector{}, ref, 0); err != nil || !chunk.Stale {
		t.Fatalf("bound ref fell back to a management target: %+v %v", chunk, err)
	}
	a.ReleaseSessionHistoryRead(handle.ID)
	// Even reopening the same physical source does not rebind an old ref.
	next, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.ReleaseSessionHistoryRead(next.ID)
	if chunk := a.HistoryContentForTab(tab.ID, ref, 0); !chunk.Stale || chunk.Data != "" {
		t.Fatal("released content ref adopted the successor's reader")
	}
	after, _ := os.ReadFile(tab.SessionPath)
	afterEvent, _ := os.ReadFile(store.SessionEventLog(tab.SessionPath))
	if string(after) != string(checkpoint) || string(afterEvent) != string(event) || tab.Ctrl != nil {
		t.Fatal("cold content rewrote source storage or created a controller")
	}
	if _, err := os.Stat(store.SessionDisplayIndex(tab.SessionPath)); !os.IsNotExist(err) {
		t.Fatal("cold content triggered compatibility-sidecar repair")
	}
}
