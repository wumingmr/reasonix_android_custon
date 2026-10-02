package sessionui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestComposerCASAcrossStoresDoesNotCreateRecoveryCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-ui.sqlite")
	a, b := New(path), New(path)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	r, err := a.Save(t.Context(), "composer", "local:one", "0", json.RawMessage(`{"text":"seed","future":{"keep":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Get(t.Context(), "composer", "local:one"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			_, err := s.Save(context.Background(), "composer", "local:one", r.Revision, json.RawMessage(`{"text":"edited"}`))
			failures <- err
		}(s)
	}
	wg.Wait()
	close(failures)
	conflicts := 0
	for err := range failures {
		if errors.Is(err, ErrConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflicts=%d", conflicts)
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM conflicts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("conflict copy count=%d err=%v", count, err)
	}
	_ = a.Close()
	if _, err := a.Get(t.Context(), "composer", "local:one"); err == nil {
		t.Fatal("closed store reopened after shutdown")
	}
	a = New(path)
	defer a.Close()
	r, err = a.Get(t.Context(), "composer", "local:one")
	if err != nil || r.Revision != "2" {
		t.Fatalf("restart=%+v %v", r, err)
	}
}

func TestInputDatabasePathTreatsPunctuationAsFilename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "会话 #百分号%.sqlite")
	store := New(path)
	if _, err := store.Save(t.Context(), "composer", "local:one", "0", json.RawMessage(`{"text":"keep"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	reopened := New(path)
	defer reopened.Close()
	record, err := reopened.Get(t.Context(), "composer", "local:one")
	if err != nil || string(record.Payload) != `{"text":"keep"}` {
		t.Fatalf("wrong path reopened: %+v %v", record, err)
	}
}

func TestFutureVersionIsBytePreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA user_version=99`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(path)
	defer s.Close()
	if _, err = s.Get(t.Context(), "composer", "one"); !errors.Is(err, ErrFutureVersion) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("future database changed", err)
	}
}
