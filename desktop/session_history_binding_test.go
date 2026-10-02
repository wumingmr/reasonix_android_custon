package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

func TestNativeHistoryCacheFailureDoesNotFallBackToFullReplay(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	_, tab.SessionPath = saveHistorySliceSession(t, tabSessionDir(tab), "cache-error.jsonl", []provider.Message{historySliceUser(0, "original history")})
	// The fixture writer creates a sidecar; exercise a cold source without it.
	if err := os.Remove(store.SessionDisplayIndex(tab.SessionPath)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	before, err := os.ReadFile(tab.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cache, []byte("cache unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REASONIX_CACHE_HOME", cache)
	request := normalizeHistorySliceRequest(HistorySliceRequest{Entries: 1, Turns: 1})
	if _, err := a.coldHistorySlice(tabSessionDir(tab), tab.SessionPath, request); err == nil {
		t.Fatal("cache failure silently fell back to full compatibility replay")
	}
	if _, found, stale := a.coldHistoryFieldValue(tabSessionDir(tab), tab.SessionPath, 0, 0, HistoryContentRef{Field: "content"}); found || !stale {
		t.Fatal("cache failure silently fell back to a compatibility content read")
	}
	after, err := os.ReadFile(tab.SessionPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("read failure changed authoritative history: %v", err)
	}
	if _, err := os.Stat(store.SessionDisplayIndex(tab.SessionPath)); !os.IsNotExist(err) {
		t.Fatalf("read failure wrote a compatibility sidecar: %v", err)
	}
}

func TestNativeHistoryReadersSharePreparationUntilLastRelease(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	_, tab.SessionPath = saveHistorySliceSession(t, tabSessionDir(tab), "shared.jsonl", []provider.Message{historySliceUser(0, "one"), historySliceAssistant(0, "answer")})
	// Hold admission so both readers deterministically join an unfinished job.
	resume, err := a.historyMaintenance.Foreground(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	one, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := a.historyReader(one.ID)
	second, _ := a.historyReader(two.ID)
	if first.native == nil || first.native != second.native {
		t.Fatal("readers started duplicate preparations")
	}
	a.ReleaseSessionHistoryRead(one.ID)
	if second.native.ctx.Err() != nil {
		t.Fatal("one release canceled shared preparation")
	}
	resume()
	page, err := a.ReadSessionHistorySlice(two.ID, HistorySliceRequest{Turns: 1, Entries: 2})
	if err != nil || page.Status != "ready" {
		t.Fatalf("remaining reader: %+v %v", page, err)
	}
	a.ReleaseSessionHistoryRead(two.ID)
	if second.native.ctx.Err() != context.Canceled {
		t.Fatal("last reader did not cancel job")
	}
	a.historyReaders.workers.Wait()
	if err := second.native.pager.DB.Ping(); err == nil {
		t.Fatal("last release retained SQLite handle")
	}
}

func TestHistoricalDirectoryColdReaderDoesNotStartRuntimeOrImport(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	const id = "cold-original-store"
	coldV4MigrationFixture(t, root, id)
	path := filepath.Join(root, id)
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.ctx = t.Context()
	t.Cleanup(a.closeSessionServices)
	a.tabs["cold"] = &WorkspaceTab{ID: "cold", SessionPath: path, SessionGeneration: 1}
	handle, err := a.BeginSessionHistoryReadForTab("cold")
	if err != nil {
		t.Fatal(err)
	}
	defer a.ReleaseSessionHistoryRead(handle.ID)
	reader, err := a.historyReader(handle.ID)
	if err != nil {
		t.Fatal(err)
	}
	// TitleMessages waits on the same locator preparation, giving this test a
	// deterministic completion barrier without polling a timer.
	if _, err := reader.query.TitleMessages(reader.ctx, reader.ref, 1); err != nil {
		t.Fatal(err)
	}
	page, err := a.ReadSessionHistoryWindow(handle.ID, session.HistoryWindowRequest{Anchor: "newest", Limit: 10})
	if err != nil || page.Status != "ready" || len(page.Messages) == 0 {
		t.Fatalf("cold page: %+v %v", page, err)
	}
	service, err := a.historicalSessionService(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := service.Runtime(reader.ref); exists || a.tabs["cold"].Ctrl != nil {
		t.Fatal("cold read created execution runtime")
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.SourceMappings) != 0 || len(state.PendingOperations) != 0 {
		t.Fatalf("cold read imported source: %v", err)
	}
	after, err := desktopSourceFingerprint(path)
	if err != nil || before != after {
		t.Fatal("cold read modified authoritative records")
	}
	if _, err := os.Stat(filepath.Join(config.DesktopSessionStoreDir(), id)); !os.IsNotExist(err) {
		t.Fatalf("cold read created new-format copy: %v", err)
	}
	a.ReleaseSessionHistoryRead(handle.ID)
	page, err = a.ReadSessionHistoryWindow(handle.ID, session.HistoryWindowRequest{Anchor: "newest"})
	if err != nil || page.Status != "stale_cursor" {
		t.Fatalf("released handle: %+v %v", page, err)
	}
}

func TestNativeHistorySourceReplacementRetiresOldGeneration(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	path := filepath.Join(tabSessionDir(tab), "generation.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"role\":\"user\",\"content\":\"old\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tab.SessionPath = path
	one, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := a.historyReader(one.ID)
	if page, err := a.ReadSessionHistorySlice(one.ID, HistorySliceRequest{Entries: 2}); err != nil || page.Status != "ready" {
		t.Fatalf("initial page: %+v %v", page, err)
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	if err := os.WriteFile(replacement, []byte("{\"role\":\"user\",\"content\":\"new\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	two, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := a.historyReader(two.ID)
	if first.ctx.Err() != context.Canceled {
		t.Fatal("source replacement did not cancel in-flight reads")
	}
	if first.native == second.native || first.native.key == second.native.key {
		t.Fatal("replacement reused the old preparation")
	}
	if page, err := a.ReadSessionHistorySlice(two.ID, HistorySliceRequest{Entries: 2}); err != nil || page.Status != "ready" {
		t.Fatalf("replacement page: %+v %v", page, err)
	}
	select {
	case <-first.native.closed:
	default:
		t.Fatal("new cache published before old SQLite handle closed")
	}
	if page, err := a.ReadSessionHistorySlice(one.ID, HistorySliceRequest{Entries: 2}); err != nil || page.Status != "stale_cursor" {
		t.Fatalf("old page: %+v %v", page, err)
	}
	a.ReleaseSessionHistoryRead(one.ID)
	if second.native.ctx.Err() != nil {
		t.Fatal("late old release canceled the new generation")
	}
	a.ReleaseSessionHistoryRead(two.ID)
}

func TestNativeHistoryWaitRejectsRetiredPreparation(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "preparing", true: "ready"}[ready], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			job := &nativeHistoryPreparation{ctx: ctx, done: make(chan struct{}), pager: &agent.DisplayPager{}}
			if ready {
				close(job.done)
			}
			cancel()
			pager, err := job.wait(t.Context())
			if pager != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("retired preparation returned a pager: %v, %v", pager, err)
			}
		})
	}
}

func TestNativeHistoryImmediateReopenRetainsCloseBarrier(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	_, tab.SessionPath = saveHistorySliceSession(t, tabSessionDir(tab), "reopen.jsonl", []provider.Message{historySliceUser(0, "one")})
	resume, err := a.historyMaintenance.Foreground(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	one, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := a.historyReader(one.ID)
	generation, err := nativeHistorySourceGeneration(first.path)
	if err != nil {
		t.Fatal(err)
	}
	// Delay the worker's final close bookkeeping under the owner lock, then
	// reacquire the same source before the canceled worker can retire it.
	a.historyReaders.mu.Lock()
	job := first.native
	job.cancel()
	next, release := a.acquireNativeHistoryLocked(first.path, first.head, sessionRuntimeKey(first.path), generation)
	if next == job || next.ctx.Err() != nil {
		a.historyReaders.mu.Unlock()
		t.Fatal("reopen reused a canceled preparation")
	}
	a.historyReaders.mu.Unlock()
	defer release()
	a.ReleaseSessionHistoryRead(one.ID)
	resume()
	if _, err := next.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.closed:
	default:
		t.Fatal("new preparation completed before predecessor closed")
	}
	if next.ctx.Err() != nil {
		t.Fatal("old release canceled the reopened reader")
	}
}

func TestNativeHistoryCompatibilityPageBorrowsBoundPreparation(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	_, tab.SessionPath = saveHistorySliceSession(t, tabSessionDir(tab), "compatibility.jsonl", []provider.Message{
		historySliceUser(0, "one"), historySliceAssistant(0, "answer"), historySliceUser(1, "two"),
	})
	handle, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.ReleaseSessionHistoryRead(handle.ID)
	request := normalizeHistorySliceRequest(HistorySliceRequest{Entries: 1, Turns: 1})
	bound, err := a.ReadSessionHistorySlice(handle.ID, request)
	if err != nil || bound.Status != "ready" {
		t.Fatalf("bound page: %+v %v", bound, err)
	}
	reader, _ := a.historyReader(handle.ID)
	compatibility, ready, err := a.pagedColdHistorySlice(t.Context(), tabSessionDir(tab), tab.SessionPath, request)
	if err != nil || !ready {
		t.Fatalf("compatibility page: %+v %v", compatibility, err)
	}
	if compatibility.NextCursor == "" || compatibility.NextCursor != bound.Page.NextCursor {
		t.Fatal("compatibility and bound readers used different source generations")
	}
	if reader.ctx.Err() != nil || reader.native.pager.DB.Ping() != nil {
		t.Fatal("compatibility reader released the bound reader's database")
	}
	a.historyReaders.mu.Lock()
	refs := reader.native.refs
	a.historyReaders.mu.Unlock()
	if refs != 1 {
		t.Fatalf("compatibility read leaked a preparation reference: %d", refs)
	}
}

func TestNativeHistoryShutdownCancelsUnboundPreparation(t *testing.T) {
	a := historySliceTestApp(t)
	tab := newColdHistoryTab(t, a)
	_, path := saveHistorySliceSession(t, tabSessionDir(tab), "shutdown.jsonl", []provider.Message{historySliceUser(0, "one")})
	generation, err := nativeHistorySourceGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := a.historyMaintenance.Foreground(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	a.historyReaders.mu.Lock()
	job, release := a.acquireNativeHistoryLocked(path, "", sessionRuntimeKey(path), generation)
	a.historyReaders.mu.Unlock()
	defer release()
	a.closeHistoryReaders()
	if _, err := job.wait(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown left unbound preparation active: %v", err)
	}
	select {
	case <-job.closed:
	default:
		t.Fatal("shutdown returned before cache owner closed")
	}
}
