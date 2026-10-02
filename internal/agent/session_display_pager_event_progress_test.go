package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/fileops"
	"reasonix/internal/historywork"
	"reasonix/internal/projectiondb"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

func eventResumeOptions(t *testing.T, source, cache string) (projectiondb.OpenOptions, string, int64) {
	t.Helper()
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	target, version := fileops.DiskSnapshot(source, info)
	event, err := os.Stat(store.SessionEventLog(source))
	if err != nil {
		t.Fatal(err)
	}
	eventTarget, eventVersion := fileops.DiskSnapshot(store.SessionEventLog(source), event)
	fingerprint := fmt.Sprintf("%s:%s:event:%s:%s:schema1", target.Key, version, eventTarget.Key, eventVersion)
	return projectiondb.OpenOptions{Path: cache, Migrations: displayPagerMigrations, RequireDisk: true, MaxOpenConns: 1, ResumeKey: "event-v1:" + fingerprint}, fingerprint, info.Size()
}

func TestEventPagerResumesCommittedProgress(t *testing.T) {
	for _, mode := range []string{"messages", "projection", "publication", "repeated", "process-restart", "fields-after", "damaged", "missing", "changed"} {
		t.Run(mode, func(t *testing.T) { checkEventPagerResume(t, mode) })
	}
}

func checkEventPagerResume(t *testing.T, mode string) {
	t.Helper()
	messages := make([]provider.Message, historywork.BatchEntries*3)
	for i := range messages {
		messages[i] = provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("question %d %s", i, strings.Repeat("a", 4096))}
	}
	encode := func() string {
		body, err := json.Marshal(messages)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "fields-after" {
			return `{"messages":` + string(body) + `,"schema_version":1,"type":"replace"}` + "\n"
		}
		return `{"schema_version":1,"type":"replace","messages":` + string(body) + "}\n"
	}
	text := encode()
	source, cache := eventDisplayFixture(t, text)
	opts, fingerprint, size := eventResumeOptions(t, source, cache)
	interrupt := func(wantPhase string, wantCount int) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		err := projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
			return buildEventDisplayPagerObserved(ctx, db, source, fingerprint, size, func(phase string, count int) {
				if phase == wantPhase && count == wantCount {
					cancel()
				}
			})
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interruption did not retain progress: %v", err)
		}
	}
	switch mode {
	case "publication":
		ctx, cancel := context.WithCancel(t.Context())
		err := projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
			err := buildEventDisplayPager(ctx, db, source, fingerprint, size)
			cancel()
			if err != nil {
				return err
			}
			return ctx.Err()
		})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("publication cancellation: %v", err)
		}
	case "process-restart":
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.CommandContext(t.Context(), executable, "-test.run=^TestEventPagerCrashHelper$")
		child.Env = append(os.Environ(), "REASONIX_EVENT_CRASH_SOURCE="+source, "REASONIX_EVENT_CRASH_CACHE="+cache)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("child failed: %v\n%s", err, output)
		}
	case "projection":
		interrupt("projection", historywork.BatchEntries)
	default:
		interrupt("messages", historywork.BatchEntries)
	}
	if mode == "repeated" {
		interrupt("messages", 2*historywork.BatchEntries)
	}
	if mode == "damaged" || mode == "missing" {
		pending := opts
		pending.Path += ".rebuild-pending"
		handle, err := projectiondb.Open(t.Context(), pending)
		if err != nil {
			t.Fatal(err)
		}
		query := `UPDATE metadata SET value='broken' WHERE key='event_scan_progress'`
		if mode == "missing" {
			query = `DELETE FROM metadata WHERE key='event_scan_progress'`
		}
		_, err = handle.DB.Exec(query)
		_ = handle.DB.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if mode == "changed" {
		info, err := os.Stat(store.SessionEventLog(source))
		if err != nil {
			t.Fatal(err)
		}
		for i := range messages {
			messages[i].Content = strings.ReplaceAll(messages[i].Content, "a", "b")
		}
		text = encode()
		if err := os.WriteFile(store.SessionEventLog(source), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(store.SessionEventLog(source), info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	var meter historywork.Coordinator
	pager, err := OpenDisplayPager(meter.Context(t.Context()), source, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer pager.Close()
	digest, err := ContentDigestForMessages(messages)
	if err != nil || pager.Header.ContentDigest != digest || pager.Header.MessageCount != len(messages) {
		t.Fatalf("resumed projection changed semantic content: %+v %v", pager.Header, err)
	}
	read := meter.Diagnostics().InstrumentedReadBytes
	if mode == "projection" && read >= int64(len(text))*3/4 {
		t.Fatalf("projection prefix decoded again: %d of %d", read, len(text))
	}
	if (mode == "messages" || mode == "process-restart" || mode == "repeated") && read >= int64(len(text))*19/10 {
		t.Fatalf("message scan prefix decoded again: %d of %d", read, len(text))
	}
	page, err := pager.EventMessages(len(messages)-1, len(messages))
	if err != nil || len(page) != 1 || page[0].Content != messages[len(messages)-1].Content {
		t.Fatalf("wrong resumed source locations: %+v %v", page, err)
	}
	after, err := os.ReadFile(store.SessionEventLog(source))
	if err != nil || string(after) != text {
		t.Fatalf("authoritative source was changed: %v", err)
	}
}

func TestEventPagerCrashHelper(t *testing.T) {
	source, cache := os.Getenv("REASONIX_EVENT_CRASH_SOURCE"), os.Getenv("REASONIX_EVENT_CRASH_CACHE")
	if source == "" || cache == "" {
		return
	}
	opts, fingerprint, size := eventResumeOptions(t, source, cache)
	err := projectiondb.Rebuild(t.Context(), opts, func(ctx context.Context, db *sql.DB) error {
		return buildEventDisplayPagerObserved(ctx, db, source, fingerprint, size, func(phase string, count int) {
			if phase == "messages" && count == historywork.BatchEntries {
				os.Exit(0)
			}
		})
	})
	t.Fatalf("child did not exit: %v", err)
}

func TestEventPagerRejectsSourceChangeBeforePublication(t *testing.T) {
	for _, mode := range []string{"event-rewrite", "event-replace", "checkpoint-rewrite"} {
		t.Run(mode, func(t *testing.T) {
			source, cache := eventDisplayFixture(t, `{"schema_version":1,"type":"replace","messages":[{"role":"user","content":"old"}]}`)
			opts, fingerprint, size := eventResumeOptions(t, source, cache)
			err := projectiondb.Rebuild(t.Context(), opts, func(ctx context.Context, db *sql.DB) error {
				return buildEventDisplayPagerObserved(ctx, db, source, fingerprint, size, func(phase string, _ int) {
					if phase != "projection" {
						return
					}
					path := store.SessionEventLog(source)
					if mode == "checkpoint-rewrite" {
						path = source
					}
					info, _ := os.Stat(path)
					body, _ := os.ReadFile(path)
					if mode == "event-replace" {
						if err := os.Rename(path, filepath.Join(filepath.Dir(path), "previous")); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(body), "old", "new")), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
				})
			})
			if !errors.Is(err, ErrDisplaySourceChanged) {
				t.Fatalf("changed source was published: %v", err)
			}
		})
	}
}

func TestEventPagerResumedArrayPreservesJSONSemantics(t *testing.T) {
	for _, mode := range []string{"duplicate-messages", "append-replace", "bad-separator", "trailing-comma", "broken-tail", "last-element"} {
		t.Run(mode, func(t *testing.T) {
			messages := make([]provider.Message, historywork.BatchEntries)
			for i := range messages {
				messages[i] = provider.Message{Role: provider.RoleUser, Content: fmt.Sprint(i)}
			}
			body, _ := json.Marshal(messages)
			prefix := `{"schema_version":1,"type":"replace","messages":`
			text := prefix + string(body) + "}\n"
			want := len(messages)
			switch mode {
			case "duplicate-messages":
				text = prefix + string(body) + `,"messages":[{"role":"user","content":"last field wins"}]}`
				want = 1
			case "append-replace":
				text += `{"schema_version":1,"type":"append","message_index":128,"messages":[{"role":"assistant","content":"append"}]}` + "\n" +
					`{"schema_version":1,"type":"replace","messages":[{"role":"user","content":"replacement"}]}`
				want = 1
			case "bad-separator":
				text = prefix + string(body[:len(body)-1]) + ` {"role":"user","content":"missing comma"}]}`
			case "trailing-comma":
				text = prefix + string(body[:len(body)-1]) + `,]}`
			case "broken-tail":
				text = prefix + string(body)
			}
			source, cache := eventDisplayFixture(t, text)
			opts, fingerprint, size := eventResumeOptions(t, source, cache)
			ctx, cancel := context.WithCancel(t.Context())
			err := projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
				return buildEventDisplayPagerObserved(ctx, db, source, fingerprint, size, func(phase string, count int) {
					if phase == "messages" && count == historywork.BatchEntries {
						cancel()
					}
				})
			})
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected checkpoint cancellation: %v", err)
			}
			pager, err := OpenDisplayPager(t.Context(), source, cache)
			if mode == "bad-separator" || mode == "trailing-comma" || mode == "broken-tail" {
				if err == nil {
					pager.Close()
					t.Fatal("invalid tail was published")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer pager.Close()
			if pager.Header.MessageCount != want {
				t.Fatalf("resumed event semantics changed: %d != %d", pager.Header.MessageCount, want)
			}
		})
	}
}
