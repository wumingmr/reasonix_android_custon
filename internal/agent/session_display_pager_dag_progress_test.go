package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"reasonix/internal/fileops"
	"reasonix/internal/historywork"
	"reasonix/internal/projectiondb"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

func dagResumeOptions(t *testing.T, source, cache, head string) (projectiondb.OpenOptions, string, int64) {
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
	fingerprint := fmt.Sprintf("%s:%s:event:%s:%s:dag:%d:%d:%s", target.Key, version, eventTarget.Key, eventVersion, event.Size(), event.ModTime().UnixNano(), head)
	return projectiondb.OpenOptions{Path: cache, Migrations: displayPagerMigrations, RequireDisk: true, MaxOpenConns: 1, ResumeKey: "dag-v1:" + fingerprint}, fingerprint, info.Size()
}

func dagResumeFixture(t *testing.T) (string, string) {
	t.Helper()
	source := dagTestSession(t)
	if err := os.WriteFile(source, nil, 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000000, 0).UTC()
	system, err := encodeSessionDAGMessage(dagMsg(provider.RoleSystem, "inherited system", "sys"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []sessionDAGEntry{{Type: sessionDAGTypeSystem, Head: SessionMainHead, Msgs: system, At: at},
		{Type: sessionDAGTypeFork, Head: SessionMainHead, NewHead: "fork", At: at},
		{Type: sessionDAGTypeSelect, Head: "fork", At: at}}
	parent := ""
	for i := range 3 * historywork.BatchEntries {
		id := fmt.Sprintf("node-%d", i)
		entries = append(entries, dagMessageEntry(t, "fork", parent, "turn", dagMsg(provider.RoleUser, fmt.Sprintf("question %d %s", i, strings.Repeat("a", 4096)), id), at))
		parent = id
	}
	patch, err := encodeSessionDAGMessage(dagMsg(provider.RoleUser, "patched", "node-5"))
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, sessionDAGEntry{Type: sessionDAGTypePatch, Target: "node-5", Msgs: patch, At: at})
	dagAppend(t, source, entries...)
	return source, filepath.Join(t.TempDir(), "dag.sqlite")
}

func TestDAGPagerResumesAllDurablePhases(t *testing.T) {
	for _, phase := range []string{"scan", "chain", "projection"} {
		for _, mode := range []string{"cancel", "restart", "repeat", "missing", "damaged"} {
			t.Run(phase+"/"+mode, func(t *testing.T) { checkDAGPagerResume(t, phase, mode) })
		}
	}
}

func checkDAGPagerResume(t *testing.T, phase, mode string) {
	t.Helper()
	source, cache := dagResumeFixture(t)
	original, err := os.ReadFile(store.SessionEventLog(source))
	if err != nil {
		t.Fatal(err)
	}
	opts, fingerprint, size := dagResumeOptions(t, source, cache, "")
	interrupt := func(threshold int) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		err := projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
			return buildDAGDisplayPagerObserved(ctx, db, source, fingerprint, "", size, func(stage string, count int) {
				if stage == phase && count >= threshold {
					cancel()
				}
			})
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	if mode == "restart" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.CommandContext(t.Context(), executable, "-test.run=^TestDAGPagerCrashHelper$")
		child.Env = append(os.Environ(), "REASONIX_DAG_CRASH_SOURCE="+source, "REASONIX_DAG_CRASH_CACHE="+cache, "REASONIX_DAG_CRASH_PHASE="+phase)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("child failed: %v\n%s", err, output)
		}
	} else {
		interrupt(historywork.BatchEntries)
	}
	if mode == "repeat" {
		interrupt(2 * historywork.BatchEntries)
	}
	if mode == "missing" || mode == "damaged" {
		pending := opts
		pending.Path += ".rebuild-pending"
		handle, err := projectiondb.Open(t.Context(), pending)
		if err != nil {
			t.Fatal(err)
		}
		query := `UPDATE metadata SET value='broken' WHERE key='dag_progress'`
		if mode == "missing" {
			query = `DELETE FROM metadata WHERE key='dag_progress'`
		}
		_, err = handle.DB.Exec(query)
		_ = handle.DB.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	var meter historywork.Coordinator
	pager, err := OpenDisplayPager(meter.Context(t.Context()), source, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer pager.Close()
	read := meter.Diagnostics().InstrumentedReadBytes
	if mode != "missing" && mode != "damaged" {
		// Capture accounting before asserting pages: projection resume must not
		// repeat the graph scan, and scan resume must skip its validated prefix.
		limit := int64(len(original)) * 25 / 10
		switch phase {
		case "chain":
			limit = int64(len(original)) * 11 / 10
		case "projection":
			limit = int64(len(original)) * 9 / 10
		}
		if read >= limit {
			t.Fatalf("completed %s prefix read again: %d >= %d", phase, read, limit)
		}
	}
	assertDAGPagerReplay(t, pager, source, "fork")
	after, err := os.ReadFile(store.SessionEventLog(source))
	if err != nil || string(after) != string(original) {
		t.Fatalf("source changed: %v", err)
	}
}

func assertDAGPagerReplay(t *testing.T, pager *DisplayPager, source, head string) {
	t.Helper()
	want, _ := dagReplay(t, source).materialize(head)
	got, err := pager.DAGMessages(0, pager.Header.MessageCount)
	if err != nil || len(got) != len(want) {
		t.Fatalf("resumed page length %d != %d: %v", len(got), len(want), err)
	}
	for i := range got {
		got[i].CreatedAt = want[i].CreatedAt
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("resumed view differs from native replay")
	}
	digest, err := ContentDigestForMessages(want)
	if err != nil || pager.Header.ContentDigest != digest {
		t.Fatalf("resumed digest mismatch: %v", err)
	}
}

func TestDAGPagerCrashHelper(t *testing.T) {
	source, cache, phase := os.Getenv("REASONIX_DAG_CRASH_SOURCE"), os.Getenv("REASONIX_DAG_CRASH_CACHE"), os.Getenv("REASONIX_DAG_CRASH_PHASE")
	if source == "" || cache == "" {
		return
	}
	opts, fingerprint, size := dagResumeOptions(t, source, cache, "")
	err := projectiondb.Rebuild(t.Context(), opts, func(ctx context.Context, db *sql.DB) error {
		return buildDAGDisplayPagerObserved(ctx, db, source, fingerprint, "", size, func(stage string, count int) {
			if stage == phase && count >= historywork.BatchEntries {
				os.Exit(0)
			}
		})
	})
	t.Fatalf("child did not exit: %v", err)
}

func TestDAGPagerResumeAfterPopulateBeforePublish(t *testing.T) {
	source, cache := dagResumeFixture(t)
	opts, fingerprint, size := dagResumeOptions(t, source, cache, "")
	ctx, cancel := context.WithCancel(t.Context())
	err := projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
		if err := buildDAGDisplayPager(ctx, db, source, fingerprint, "", size); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var meter historywork.Coordinator
	pager, err := OpenDisplayPager(meter.Context(t.Context()), source, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer pager.Close()
	if meter.Diagnostics().InstrumentedReadBytes > historywork.ReadChunk {
		t.Fatal("completed projection decoded again before publication")
	}
	assertDAGPagerReplay(t, pager, source, "fork")
}
