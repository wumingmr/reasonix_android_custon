package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type catalogOnlyPersistence struct{ opens atomic.Int32 }

func (*catalogOnlyPersistence) Create(CreateOptions) (*Session, error) {
	return nil, errors.New("unexpected create")
}
func (p *catalogOnlyPersistence) Open(string, AccessMode) (*Session, error) {
	p.opens.Add(1)
	return nil, errors.New("unexpected history open")
}
func (*catalogOnlyPersistence) Stat(context.Context, string) (SessionInfo, error) {
	return SessionInfo{}, errors.New("unexpected stat")
}
func (*catalogOnlyPersistence) List(context.Context, string, int) (SessionPage, error) {
	return SessionPage{Sessions: []SessionInfo{{
		SessionID: "listed", Codec: Codec, Title: "cached", Turns: 3,
		MetadataStatus: MetadataReady,
	}}}, nil
}

func TestListDoesNotOpenSessionHistoryForReadyMetadata(t *testing.T) {
	persistence := &catalogOnlyPersistence{}
	query := newQuery("local", persistence, nil)
	page, err := query.List(t.Context(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Title != "cached" || page.Sessions[0].Turns != 3 {
		t.Fatalf("page = %+v", page)
	}
	if got := persistence.opens.Load(); got != 0 {
		t.Fatalf("catalog listing opened %d full histories", got)
	}
}

func TestResolveSessionIDReturnsCatalogOwnedIdentity(t *testing.T) {
	persistence := &catalogOnlyPersistence{}
	query := newQuery("local", persistence, nil)
	ref, err := query.ResolveSessionID(t.Context(), "listed")
	if err != nil {
		t.Fatal(err)
	}
	if ref != (SessionRef{HostID: "local", SessionID: "listed"}) {
		t.Fatalf("resolved ref = %+v", ref)
	}
	if _, err = query.ResolveSessionID(t.Context(), "../listed"); err == nil {
		t.Fatal("path-like session identity was accepted")
	}
	if _, err = query.ResolveSessionID(t.Context(), "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing identity error = %v", err)
	}
}

func TestCatalogMetadataIsBoundToSessionIncarnation(t *testing.T) {
	cacheDir := t.TempDir()
	first := Manifest{SessionID: "same-id", CreatedAt: time.Unix(1, 0).UTC()}
	if err := writeCatalogMetadata(cacheDir, metadataFromProjection(first, 0, Projection{Title: "stale"})); err != nil {
		t.Fatal(err)
	}
	second := Manifest{SessionID: "same-id", CreatedAt: time.Unix(2, 0).UTC()}
	if _, err := readCatalogMetadata(cacheDir, second, logRevision{}); err == nil {
		t.Fatal("metadata from a deleted session was reused by a new incarnation")
	}
}

func TestCatalogMetadataIsBoundToLogRevision(t *testing.T) {
	cacheDir := t.TempDir()
	manifest := Manifest{SessionID: "rev", CreatedAt: time.Unix(1, 0).UTC()}
	written := metadataFromProjection(manifest, 4, Projection{Title: "cached"})
	written.LogSize, written.LogModTimeNS, written.LogIdentity = 128, 7, "digest"
	if err := writeCatalogMetadata(cacheDir, written); err != nil {
		t.Fatal(err)
	}
	metadata, err := readCatalogMetadata(cacheDir, manifest, logRevision{Size: 128, ModTimeNS: 7, Identity: "digest", Exists: true})
	if err != nil || metadata.Title != "cached" || metadata.Sequence != 4 {
		t.Fatalf("matching revision rejected: %+v, %v", metadata, err)
	}
	// A log that grew by even one byte invalidates the projection without any
	// need to replay it.
	if _, err := readCatalogMetadata(cacheDir, manifest, logRevision{Size: 129, ModTimeNS: 7, Identity: "digest", Exists: true}); err == nil {
		t.Fatal("stale log revision was accepted")
	}
}

type pagedCatalogHandle struct{ reads int }

func (h *pagedCatalogHandle) Read(_ context.Context, offset uint64, _ int) (EventPage, error) {
	h.reads++
	if offset == 0 {
		return EventPage{Commits: []Commit{{FirstSequence: 1, EventCount: 1, Events: []Event{{Kind: "diagnostic", Optional: true, Sequence: 1}}}}, Next: 1, Truncated: true}, nil
	}
	return EventPage{Commits: []Commit{{FirstSequence: 2, EventCount: 1, Events: []Event{{Kind: "diagnostic", Optional: true, Sequence: 2}}}}, Next: 2}, nil
}
func (*pagedCatalogHandle) Append(context.Context, Batch) (Commit, error) {
	return Commit{}, ErrReadOnly
}
func (*pagedCatalogHandle) Flush(context.Context) (DurableReceipt, error) {
	return DurableReceipt{}, nil
}
func (*pagedCatalogHandle) Close(context.Context) error { return nil }

func TestCatalogMetadataRebuildAdvancesPagedCursor(t *testing.T) {
	handle := &pagedCatalogHandle{}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	manifest := Manifest{SessionID: "paged", CreatedAt: time.Unix(1, 0).UTC()}
	if err := rebuildCatalogMetadata(t.Context(), handle, cacheDir, t.TempDir(), manifest); err != nil {
		t.Fatal(err)
	}
	if handle.reads != 2 {
		t.Fatalf("reads = %d, want 2", handle.reads)
	}
	if _, err := os.Stat(catalogMetadataPath(cacheDir)); err != nil {
		t.Fatal(err)
	}
}

// TestWarmListDoesNotReplayEventBodies proves catalog listing never falls back
// to a log scan. Building the sparse offset index is the observable witness: it
// only happens when a caller replays events, so its absence after List shows the
// page was served from the manifest head, the log revision, and the metadata
// cache alone.
func TestWarmListDoesNotReplayEventBodies(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	persistence := NewFilesystemPersistence(root)
	session, err := persistence.Create(CreateOptions{SessionID: "warm"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"title": "Warm title"})
	if _, err := session.AppendBatch(t.Context(), "title", []Event{{Kind: "session/title", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(t.Context(), Batch{OperationID: "turn", TurnID: "turn-1", Events: []Event{
		{Kind: "turn/start"}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(root, ".query-cache", "warm")
	indexPath := filepath.Join(cacheDir, "events.offset-index.json")
	if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	service, err := NewService("local", persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	page, err := service.Query().List(t.Context(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 {
		t.Fatalf("sessions = %+v", page.Sessions)
	}
	got := page.Sessions[0]
	if got.MetadataStatus != MetadataReady || got.Title != "Warm title" || got.Turns != 1 || got.EventSequence != 3 {
		t.Fatalf("warm listing = %+v", got)
	}
	if _, statErr := os.Stat(indexPath); statErr == nil {
		t.Fatal("warm List rebuilt the sparse offset index, so it replayed event bodies")
	} else if !os.IsNotExist(statErr) {
		t.Fatal(statErr)
	}
}
