package draftstate

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestUnrecognizedDraftDatabaseIsNeverInitializedOrRewritten(t *testing.T) {
	for _, version := range []int{-1, 0, SchemaVersion + 1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "drafts.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{`CREATE TABLE future_input(body TEXT)`, `INSERT INTO future_input VALUES('unsent content')`, fmt.Sprintf("PRAGMA user_version=%d", version)} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				store := New(path)
				_, err := store.ListActive(t.Context())
				_ = store.Close()
				if !errors.Is(err, ErrUnsupportedVersion) {
					t.Fatalf("unrecognized database should be blocked, got %v", err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("unrecognized database was rewritten: %v", err)
				}
			}
		})
	}
}
