package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"reasonix/internal/fileops"
	"reasonix/internal/historywork"
	"reasonix/internal/projectiondb"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

func TestDisplayPagerImportResumesCommittedEntries(t *testing.T) {
	for _, mode := range []string{"header-first", "entries-first", "public-open", "last-entry", "two-cancels", "damaged-progress", "changed-source"} {
		t.Run(mode, func(t *testing.T) {
			messages := make([]provider.Message, 1536)
			for i := range messages {
				messages[i] = provider.Message{Role: provider.RoleUser, Content: "original message"}
			}
			digest, err := digestSessionMessages(messages)
			if err != nil {
				t.Fatal(err)
			}
			idx := BuildSessionDisplayIndex(messages, 3, true, digest)
			indexPath := filepath.Join(t.TempDir(), "old.display-index.json")
			source := filepath.Join(filepath.Dir(indexPath), "old.jsonl")
			fingerprint := "source"
			if mode == "public-open" {
				indexPath = store.SessionDisplayIndex(source)
				if err := writeSessionMessages(source, messages); err != nil {
					t.Fatal(err)
				}
				if err := SaveBranchMeta(source, BranchMeta{Revision: 3, ContentDigest: digestString(digest)}); err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.MarshalIndent(idx, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "entries-first" {
				var header map[string]json.RawMessage
				if err := json.Unmarshal(body, &header); err != nil {
					t.Fatal(err)
				}
				entries := header["entries"]
				delete(header, "entries")
				rest, err := json.Marshal(header)
				if err != nil {
					t.Fatal(err)
				}
				body = append(append([]byte(`{"entries":`), entries...), append([]byte{','}, rest[1:]...)...)
			}
			if err := os.WriteFile(indexPath, body, 0600); err != nil {
				t.Fatal(err)
			}
			opts := projectiondb.OpenOptions{Path: filepath.Join(t.TempDir(), "display.sqlite"), Migrations: displayPagerMigrations, RequireDisk: true, MaxOpenConns: 1, ResumeKey: "same-source"}
			if mode == "public-open" {
				info, err := os.Stat(source)
				if err != nil {
					t.Fatal(err)
				}
				after := info.ModTime().Add(time.Second)
				if err := os.Chtimes(indexPath, after, after); err != nil {
					t.Fatal(err)
				}
				indexInfo, err := os.Stat(indexPath)
				if err != nil {
					t.Fatal(err)
				}
				target, version := fileops.DiskSnapshot(source, info)
				fingerprint = fmt.Sprintf("%s:%s:%d:%d", target.Key, version, indexInfo.Size(), indexInfo.ModTime().UnixNano())
				opts.ResumeKey = "display-import-v1:" + fingerprint
			}
			ctx, cancel := context.WithCancel(t.Context())
			stopAt := 1024
			if mode == "last-entry" {
				stopAt = len(messages)
			}
			err = projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
				return importDisplayPagerObserved(ctx, db, indexPath, fingerprint, func(count int) {
					if count == stopAt {
						cancel()
					}
				})
			})
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("interrupted import: %v", err)
			}
			if mode == "two-cancels" {
				ctx, cancel := context.WithCancel(t.Context())
				err = projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
					return importDisplayPagerObserved(ctx, db, indexPath, fingerprint, func(count int) {
						if count != stopAt+historywork.BatchEntries {
							t.Fatalf("resumed at unexpected position: %d", count)
						}
						cancel()
					})
				})
				cancel()
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("second interrupted import: %v", err)
				}
			}
			if mode == "damaged-progress" {
				pending := opts
				pending.Path += ".rebuild-pending"
				handle, err := projectiondb.Open(t.Context(), pending)
				if err != nil {
					t.Fatal(err)
				}
				_, err = handle.DB.Exec(`UPDATE metadata SET value='broken' WHERE key='display_import_progress'`)
				_ = handle.DB.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-source" {
				info, err := os.Stat(indexPath)
				if err != nil {
					t.Fatal(err)
				}
				body = bytes.ReplaceAll(body, []byte("original message"), []byte("replaced message"))
				idx.ListingPreview = "replaced message"
				if err := os.WriteFile(indexPath, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(indexPath, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			meter := &historywork.Coordinator{}
			first := 0
			if mode == "public-open" {
				pager, err := OpenDisplayPager(meter.Context(t.Context()), source, opts.Path)
				if err != nil {
					t.Fatal(err)
				}
				_ = pager.Close()
				if pager.Built || pager.Header.ContentDigest != idx.ContentDigest {
					t.Fatal("public reader did not retain the imported generation")
				}
			} else {
				err = projectiondb.Rebuild(meter.Context(t.Context()), opts, func(ctx context.Context, db *sql.DB) error {
					return importDisplayPagerObserved(ctx, db, indexPath, fingerprint, func(count int) {
						if first == 0 {
							first = count
						}
					})
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "changed-source", "damaged-progress":
				if first != historywork.BatchEntries {
					t.Fatalf("invalid generation resumed at %d", first)
				}
			default:
				if got := meter.Diagnostics().InstrumentedReadBytes; got >= int64(len(body))*3/4 {
					t.Fatalf("completed prefix read again: %d/%d", got, len(body))
				}
			}
			handle, err := projectiondb.Open(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.DB.Close()
			pager := &DisplayPager{DB: handle.DB, ctx: t.Context()}
			last, err := pager.Entry(len(messages) - 1)
			if err != nil || !reflect.DeepEqual(last, idx.Entries[len(messages)-1]) {
				t.Fatalf("resumed entry changed: %+v %v", last, err)
			}
			var header string
			if err := handle.DB.QueryRow(`SELECT value FROM metadata WHERE key='header'`).Scan(&header); err != nil {
				t.Fatal(err)
			}
			var got SessionDisplayIndex
			if err := json.Unmarshal([]byte(header), &got); err != nil {
				t.Fatal(err)
			}
			idx.Entries = nil
			if !reflect.DeepEqual(&got, idx) {
				t.Fatalf("header changed: %+v want %+v", got, idx)
			}
			after, err := os.ReadFile(indexPath)
			if err != nil || !bytes.Equal(after, body) {
				t.Fatalf("import changed old index: %v", err)
			}
		})
	}
}

func TestDisplayPagerImportResumeFramingRejectsInvalidSeparators(t *testing.T) {
	for _, tail := range []string{",]}", ", ,{}]}", "{}]}", "garbage"} {
		t.Run(tail, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index")
			if err := os.WriteFile(path, []byte("prefix"+tail), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, _, err := displayImportDecoder(t.Context(), f, int64(len("prefix"))); err == nil {
				t.Fatal("invalid continuation accepted")
			}
		})
	}
}
