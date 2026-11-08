package workspacelease

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/filelock"
)

func tryOwners(t *testing.T) (*Owner, *Owner, *atomic.Int32) {
	t.Helper()
	root, locks := t.TempDir(), t.TempDir()
	var notices atomic.Int32
	first, err := New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root, locks, func() { notices.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.EndRun(); second.EndRun() })
	return first, second, &notices
}

func TestTryWriteSameFileConflictDoesNotWait(t *testing.T) {
	first, second, notices := tryOwners(t)
	path := filepath.Join(first.rootPath, "same.go")
	release, err := first.HoldWriteForPath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = second.TryHoldWriteForPaths(ctx, []string{path})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Scope != "file" || conflict.Target != path || ctx.Err() != nil {
		t.Fatalf("non-waiting file conflict = %v, context = %v", err, ctx.Err())
	}
	if second.State().Waiting || second.State().Acquired || notices.Load() != 0 {
		t.Fatalf("conflict queued or leaked: %+v, notices=%d", second.State(), notices.Load())
	}
	release()
	retry, err := second.TryHoldWriteForPaths(ctx, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	retry()
}

func TestTryWriteDifferentFilesOverlap(t *testing.T) {
	first, second, _ := tryOwners(t)
	a := filepath.Join(first.rootPath, "a.go")
	b := filepath.Join(first.rootPath, "b.go")
	for first.pathLockPath(a) == first.pathLockPath(b) {
		b += "b"
	}
	release, err := first.TryHoldWriteForPaths(context.Background(), []string{a})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other, err := second.TryHoldWriteForPaths(context.Background(), []string{b})
	if err != nil {
		t.Fatalf("different file blocked by an active same-directory writer: %v", err)
	}
	other()
}

func TestTryWriteWorkspaceAndQueueConflict(t *testing.T) {
	for _, queue := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy workspace", true: "priority queue"}[queue], func(t *testing.T) {
			first, second, notices := tryOwners(t)
			lock := first.lockPath
			if queue {
				lock += ".queue"
			}
			release, err := filelock.TryAcquire(lock)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = second.TryHoldWriteForPaths(ctx, []string{filepath.Join(first.rootPath, "a.go")})
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.Scope != "workspace" || ctx.Err() != nil || notices.Load() != 0 {
				t.Fatalf("workspace/queue conflict=%v context=%v notices=%d", err, ctx.Err(), notices.Load())
			}
		})
	}
}

func TestTryWriteRollbackAndLocalPromotion(t *testing.T) {
	first, second, _ := tryOwners(t)
	paths := []string{filepath.Join(first.rootPath, "a.go"), filepath.Join(first.rootPath, "b.go")}
	specs, err := first.pathSpecs(paths)
	if err != nil {
		t.Fatal(err)
	}
	for specs[0].slot == specs[1].slot {
		paths[1] += "b"
		specs, err = first.pathSpecs(paths)
		if err != nil {
			t.Fatal(err)
		}
	}
	release, err := first.TryHoldWriteForPaths(context.Background(), []string{specs[1].access})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := first.TryHoldWrite(ctx); !errors.Is(err, filelock.ErrHeld) || ctx.Err() != nil {
		t.Fatalf("local promotion queued: %v", err)
	}
	if _, err := second.TryHoldWriteForPaths(ctx, paths); !errors.Is(err, filelock.ErrHeld) {
		t.Fatalf("multi-file conflict = %v", err)
	}
	probe, err := second.TryHoldWriteForPaths(ctx, []string{specs[0].access})
	if err != nil {
		t.Fatalf("failed group leaked first path lock: %v", err)
	}
	probe()
	reentered, err := first.TryHoldWriteForPaths(ctx, []string{specs[1].access})
	if err != nil {
		t.Fatalf("Owner reentry: %v", err)
	}
	reentered()
}

func TestTryWriteCanceledContext(t *testing.T) {
	first, _, _ := tryOwners(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := first.TryHoldWrite(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled workspace hold = %v", err)
	}
	if _, err := first.TryHoldWriteForPaths(ctx, []string{filepath.Join(first.rootPath, "a.go")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled path hold = %v", err)
	}
}

func TestTryWriteCrossProcessHelper(t *testing.T) {
	if os.Getenv("REASONIX_TRY_LEASE_HELPER") != "1" {
		return
	}
	o, err := New(os.Getenv("REASONIX_TRY_ROOT"), os.Getenv("REASONIX_TRY_LOCKS"), nil)
	if err != nil {
		t.Fatal(err)
	}
	release, err := o.TryHoldWriteForPaths(context.Background(), []string{filepath.Join(o.rootPath, "same.go")})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.WriteFile(os.Getenv("REASONIX_TRY_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
}

func TestTryWriteCrossProcessConflict(t *testing.T) {
	first, second, notices := tryOwners(t)
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTryWriteCrossProcessHelper$")
	cmd.Env = append(os.Environ(), "REASONIX_TRY_LEASE_HELPER=1", "REASONIX_TRY_ROOT="+first.rootPath,
		"REASONIX_TRY_LOCKS="+first.lockDir, "REASONIX_TRY_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire file hold")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := second.TryHoldWriteForPaths(ctx, []string{filepath.Join(first.rootPath, "same.go")})
	if !errors.Is(err, filelock.ErrHeld) || ctx.Err() != nil || notices.Load() != 0 {
		t.Fatalf("cross-process conflict = %v, context = %v", err, ctx.Err())
	}
	other := filepath.Join(first.rootPath, "other.go")
	for first.pathLockPath(other) == first.pathLockPath(filepath.Join(first.rootPath, "same.go")) {
		other += "x"
	}
	release, err := second.TryHoldWriteForPaths(ctx, []string{other})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := second.TryHoldWrite(ctx); !errors.Is(err, filelock.ErrHeld) || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("opaque write did not protect child file: %v", err)
	}
}
