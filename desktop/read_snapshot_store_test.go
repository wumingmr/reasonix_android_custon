package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func mustListProjectTree(t *testing.T, a *App) []ProjectNode {
	t.Helper()
	nodes, err := a.ListProjectTree()
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func TestReadSnapshotFrozenWindows(t *testing.T) {
	for _, count := range []int{205, 405, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var store readSnapshotStore
			defer store.close()
			snap, err := store.build(t.Context(), "query", func(ctx context.Context, snap *readSnapshot) error {
				for n := range count {
					if err := store.append(ctx, snap, n); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			cursor, total := "", 0
			for {
				var values []int
				next, id, _, _, err := store.page(t.Context(), "query", cursor, snap, 200, func(b []byte) error {
					var n int
					if err := json.Unmarshal(b, &n); err != nil {
						return err
					}
					values = append(values, n)
					return nil
				})
				if err != nil || id != snap.id {
					t.Fatalf("page: %s %v", id, err)
				}
				for _, n := range values {
					if n != total {
						t.Fatalf("row=%d want %d", n, total)
					}
					total++
				}
				if next == "" {
					break
				}
				cursor = next
			}
			if total != count {
				t.Fatalf("rows=%d", total)
			}
			if _, _, _, _, err := store.page(t.Context(), "other", cursor, snap, 1, func([]byte) error { return nil }); err == nil {
				t.Fatal("cross-query cursor accepted")
			}
		})
	}
}

func TestReadSnapshotSpillExpiryAndRelease(t *testing.T) {
	var store readSnapshotStore
	defer store.close()
	snap, err := store.build(t.Context(), "spill", func(ctx context.Context, snap *readSnapshot) error {
		for range 600 {
			if err := store.append(ctx, snap, strings.Repeat("x", 1024)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.data.db == nil || snap.data.memory != 0 {
		t.Fatal("snapshot did not spill")
	}
	next, _, _, _, err := store.page(t.Context(), "spill", "", snap, 200, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	snap.lifetime.used = time.Now().Add(-readSnapshotIdle)
	store.mu.Unlock()
	if _, _, _, _, err := store.page(t.Context(), "spill", next, nil, 200, func([]byte) error { return nil }); err == nil {
		t.Fatal("expired snapshot accepted")
	}
	store.dispose(snap)
	store.close()
	if store.disk != 0 || store.memory != 0 {
		t.Fatal("release leaked storage")
	}
}

func TestReadSnapshotCancelledWaiterLeavesBuildAlive(t *testing.T) {
	var store readSnapshotStore
	defer store.close()
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := store.build(ctx, "same", func(ctx context.Context, snap *readSnapshot) error {
			close(entered)
			<-release
			return store.append(ctx, snap, 42)
		})
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	store.mu.Lock()
	job := store.building["same"]
	store.mu.Unlock()
	close(release)
	<-job.done
	if job.err != nil || job.snapshot == nil {
		t.Fatalf("shared result=%v %v", job.snapshot, job.err)
	}
}

func TestReadSnapshotSharedReleaseAndShutdown(t *testing.T) {
	app := NewApp()
	store := &app.desktopSessions.readSnapshots
	entered, finish := make(chan struct{}), make(chan struct{})
	done := make(chan *readSnapshot, 2)
	fill := func(ctx context.Context, snap *readSnapshot) error {
		close(entered)
		<-finish
		return store.append(ctx, snap, 42)
	}
	go func() {
		snap, err := store.build(t.Context(), "shared", fill)
		if err != nil {
			t.Error(err)
		}
		done <- snap
	}()
	<-entered
	// Done is consulted after joining the job and releasing the manager lock.
	secondStarted := make(chan struct{})
	go func() {
		snap, err := store.build(snapshotWaitContext{t.Context(), secondStarted}, "shared", func(ctx context.Context, snap *readSnapshot) error { return store.append(ctx, snap, 42) })
		if err != nil {
			t.Error(err)
		}
		done <- snap
	}()
	<-secondStarted
	close(finish)
	a, b := <-done, <-done
	if a == nil || b == nil || a.id == b.id || a.data != b.data {
		t.Fatal("readers need independent handles")
	}
	app.ReleaseReadSnapshot(a.id)
	app.ReleaseReadSnapshot(a.id)
	if _, _, _, _, err := store.page(t.Context(), "shared", "", b, 1, func([]byte) error { return nil }); err != nil {
		t.Fatalf("other reader was released: %v", err)
	}
	app.ReleaseReadSnapshot(b.id)
	store.close()
	store.close()
	if store.memory != 0 || store.disk != 0 {
		t.Fatal("shutdown leaked resources")
	}
	if _, err := store.build(t.Context(), "closed", fill); err == nil {
		t.Fatal("closed owner restarted")
	}
}

type snapshotWaitContext struct {
	context.Context
	joined chan struct{}
}

func (c snapshotWaitContext) Done() <-chan struct{} {
	close(c.joined)
	return c.Context.Done()
}

func TestReadSnapshotExpiredDiskReclaimedBeforeAdmission(t *testing.T) {
	var store readSnapshotStore
	defer store.close()
	fill := func(ctx context.Context, snap *readSnapshot) error {
		return store.append(ctx, snap, strings.Repeat("x", 600*1024))
	}
	for i := range 4 {
		if _, err := store.build(t.Context(), fmt.Sprint(i), fill); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	reserved := store.disk
	for _, snap := range store.entries {
		snap.lifetime.used = time.Now().Add(-readSnapshotIdle)
	}
	store.mu.Unlock()
	if reserved != readSnapshotDisk {
		t.Fatalf("reservation=%d want %d", reserved, readSnapshotDisk)
	}
	if _, err := store.build(t.Context(), "after-expiry", fill); err != nil {
		t.Fatalf("expired reservations blocked admission: %v", err)
	}
	store.close()
	if store.disk != 0 || store.memory != 0 {
		t.Fatal("expired storage leaked")
	}
}

func TestReadSnapshotShutdownWaitsForActiveAndQueuedBuilders(t *testing.T) {
	var store readSnapshotStore
	defer store.close()
	entered := make(chan struct{}, 2)
	done := make(chan error, 3)
	fill := func(ctx context.Context, snap *readSnapshot) error {
		if err := store.append(ctx, snap, strings.Repeat("x", 600*1024)); err != nil {
			return err
		}
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	for i := range 2 {
		go func() { _, err := store.build(t.Context(), fmt.Sprint(i), fill); done <- err }()
	}
	<-entered
	<-entered
	joined := make(chan struct{})
	go func() { _, err := store.build(snapshotWaitContext{t.Context(), joined}, "queued", fill); done <- err }()
	<-joined
	store.close()
	for range 3 {
		if err := <-done; err == nil {
			t.Fatal("shutdown published an unfinished snapshot")
		}
	}
	if store.disk != 0 || store.memory != 0 || len(store.entries) != 0 || len(store.building) != 0 {
		t.Fatal("shutdown returned before builder cleanup")
	}
}

func TestReadSnapshotResourceFailureDoesNotPublishPrefix(t *testing.T) {
	var store readSnapshotStore
	defer store.close()
	store.disk = readSnapshotDisk
	_, err := store.build(t.Context(), "large", func(ctx context.Context, snap *readSnapshot) error {
		for range 600 {
			if err := store.append(ctx, snap, strings.Repeat("x", 1024)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || len(store.entries) != 0 || store.memory != 0 {
		t.Fatalf("failed build leaked/published: err=%v memory=%d entries=%d", err, store.memory, len(store.entries))
	}
	store.disk = 0
}

func TestReadSnapshotEvictionKeepsBoundAndRejectsOldHandle(t *testing.T) {
	var store readSnapshotStore
	defer store.close()
	var first *readSnapshot
	for i := range 70 {
		snap, err := store.build(t.Context(), fmt.Sprint(i), func(ctx context.Context, snap *readSnapshot) error { return store.append(ctx, snap, 42) })
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = snap
		}
		if len(store.entries) > 64 {
			t.Fatal("handle bound exceeded")
		}
	}
	if _, _, _, _, err := store.page(t.Context(), "0", "", first, 1, func([]byte) error { return nil }); err == nil {
		t.Fatal("evicted handle accepted")
	}
	store.close()
	if store.memory != 0 || store.disk != 0 {
		t.Fatal("eviction leaked storage")
	}
}

func TestReadSnapshotEvictionOrdersEqualTimestampsAndPageAccess(t *testing.T) {
	var store readSnapshotStore
	defer store.close()
	var ordered []*readSnapshot
	for i := range 32 {
		handle, err := store.build(t.Context(), fmt.Sprint(i), func(ctx context.Context, snap *readSnapshot) error {
			return store.append(ctx, snap, i)
		})
		if err != nil {
			t.Fatal(err)
		}
		ordered = append(ordered, handle.data, handle)
	}
	// Reading the oldest handle must refresh its position, even when every
	// clock sample has the same value (as on a coarse platform clock).
	first := ordered[1]
	if _, _, _, _, err := store.page(t.Context(), "0", "", first, 1, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ordered = append(append(ordered[:1:1], ordered[2:]...), first)
	sharedTime := time.Now()
	store.mu.Lock()
	for _, snap := range store.entries {
		snap.lifetime.used = sharedTime
	}
	var victims []*readSnapshot
	for range ordered {
		victims = append(victims, store.evictOldestLocked())
	}
	store.mu.Unlock()
	for i, victim := range victims {
		store.dispose(victim)
		if victim != ordered[i] {
			t.Errorf("eviction %d did not follow insertion and page-access order", i)
		}
	}
}
