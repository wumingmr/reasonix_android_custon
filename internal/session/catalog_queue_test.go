package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestColdCatalogDrainsAfterOneListWhileSlotsWereBusy(t *testing.T) {
	root := t.TempDir()
	persistence := NewFilesystemPersistence(root)
	service, err := NewService("local", persistence)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		id := fmt.Sprintf("cold-%d", i)
		runtime, err := service.Create(t.Context(), CreateOptions{SessionID: id})
		if err != nil {
			t.Fatal(err)
		}
		appendRecoveryTestMessage(t, runtime.Session(), "first", "authored request")
		if err := service.Close(t.Context(), runtime.Ref()); err != nil {
			t.Fatal(err)
		}
		path := catalogMetadataPath(filepath.Join(root, ".query-cache", id))
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var old catalogMetadata
		if err := json.Unmarshal(body, &old); err != nil {
			t.Fatal(err)
		}
		old.Version = catalogMetadataVersion - 1
		if err := writeCatalogMetadata(filepath.Dir(path), old); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	query := newQuery("local", persistence, nil)
	t.Cleanup(query.Close)
	for range 2 {
		if !query.slots.tryAcquire() {
			t.Fatal("cannot hold build slots")
		}
	}
	page, err := query.List(t.Context(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 7 {
		t.Fatalf("sessions=%d", len(page.Sessions))
	}
	query.rebuildMu.Lock()
	queued, workers := len(query.rebuilding), query.metadataWorkers
	query.rebuildMu.Unlock()
	query.slots.release()
	query.slots.release()
	if queued != 7 || workers > 2 {
		t.Fatalf("queued=%d workers=%d", queued, workers)
	}
	// No second Query.List/Stat and no OpenSession: queued work must finish alone.
	query.rebuildWG.Wait()
	for _, row := range page.Sessions {
		info, err := persistence.Stat(t.Context(), row.SessionID)
		if err != nil || info.MetadataStatus != MetadataReady || info.Preview != "authored request" {
			t.Fatalf("row=%+v err=%v", info, err)
		}
	}
}

func TestCatalogCloseCancelsQueuedWorkers(t *testing.T) {
	query := newQuery("local", NewFilesystemPersistence(t.TempDir()), nil)
	query.slots.tryAcquire()
	query.slots.tryAcquire()
	for i := range 100 {
		query.scheduleMetadataRebuild(fmt.Sprintf("queued-%d", i))
	}
	query.Close()
	query.slots.release()
	query.slots.release()
	query.rebuildMu.Lock()
	defer query.rebuildMu.Unlock()
	if query.metadataWorkers != 0 || len(query.metadataQueue) != 0 || len(query.rebuilding) != 0 {
		t.Fatal("queued state survived close")
	}
}

func TestCatalogRebuildFailureIsNotAnEmptySessionOrEndlessPending(t *testing.T) {
	s, _, dir, _ := historyBoundaryFixture(t)
	if err := s.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(dir, "events.frames"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = log.Write([]byte("invalid frame header"))
	_ = log.Close()
	if err != nil {
		t.Fatal(err)
	}
	q := newQuery("local", s.persistence, nil)
	t.Cleanup(q.Close)
	if _, err := q.List(t.Context(), "", 100); err != nil {
		t.Fatal(err)
	}
	q.rebuildWG.Wait()
	for range 2 {
		page, err := q.List(t.Context(), "", 100)
		if err != nil || len(page.Sessions) != 1 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		info := page.Sessions[0]
		if info.MetadataStatus != MetadataFailed || info.Error == "" {
			t.Fatalf("failure hidden: %+v", info)
		}
	}
	q.rebuildMu.Lock()
	defer q.rebuildMu.Unlock()
	if len(q.rebuilding) != 0 {
		t.Fatal("failed history repeatedly requeued by list reads")
	}
}
