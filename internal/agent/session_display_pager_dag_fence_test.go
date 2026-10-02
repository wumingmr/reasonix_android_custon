package agent

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"reasonix/internal/historywork"
	"reasonix/internal/projectiondb"
	"reasonix/internal/store"
)

func TestDAGPagerResumeFencesSourceAndBranchChanges(t *testing.T) {
	for _, mode := range []string{"rewrite", "replace", "checkpoint", "branch", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			source, cache := dagResumeFixture(t)
			opts, fingerprint, size := dagResumeOptions(t, source, cache, "")
			ctx, cancel := context.WithCancel(t.Context())
			err := projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
				return buildDAGDisplayPagerObserved(ctx, db, source, fingerprint, "", size, func(stage string, count int) {
					if stage == "scan" && count >= historywork.BatchEntries {
						cancel()
					}
				})
			})
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			head := "fork"
			requested := ""
			if mode == "branch" {
				head, requested = SessionMainHead, SessionMainHead
			} else {
				mutateDAGPagerSource(t, source, mode)
			}
			pager, err := OpenDisplayPager(t.Context(), source, cache, requested)
			if mode == "truncated" {
				if err == nil {
					pager.Close()
					t.Fatal("truncated source published as complete")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer pager.Close()
			assertDAGPagerReplay(t, pager, source, head)
		})
	}
}

func TestDAGPagerRejectsSourceChangeBeforePublication(t *testing.T) {
	for _, mode := range []string{"rewrite", "replace", "checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			source, cache := dagResumeFixture(t)
			opts, fingerprint, size := dagResumeOptions(t, source, cache, "")
			changed := false
			err := projectiondb.Rebuild(t.Context(), opts, func(ctx context.Context, db *sql.DB) error {
				return buildDAGDisplayPagerObserved(ctx, db, source, fingerprint, "", size, func(stage string, count int) {
					if !changed && stage == "projection" && count >= historywork.BatchEntries {
						changed = true
						mutateDAGPagerSource(t, source, mode)
					}
				})
			})
			if !changed || !errors.Is(err, ErrDisplaySourceChanged) {
				t.Fatalf("changed source accepted: %v", err)
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatalf("changed projection published: %v", err)
			}
		})
	}
}

func mutateDAGPagerSource(t *testing.T, source, mode string) {
	t.Helper()
	path := store.SessionEventLog(source)
	if mode == "checkpoint" {
		path = source
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.ReplaceAll(body, []byte("question"), []byte("QUESTION"))
	switch mode {
	case "checkpoint":
		body = []byte("changed checkpoint")
	case "truncated":
		body = append(body, []byte(`{"schema_version":2,`)...)
	}
	writePath := path
	if mode == "replace" {
		writePath += ".replacement"
	}
	if err := os.WriteFile(writePath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if mode == "replace" {
		// Windows rejects direct replacement of an open target even with delete
		// sharing. Moving the old path first still tests fencing the new identity.
		if err := os.Rename(path, path+".previous"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(writePath, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}
