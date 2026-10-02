package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFilesystemCreateIsExclusiveAndOpenNeverCreates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	persistence := NewFilesystemPersistence(root)

	if _, err := persistence.Open("missing", ReadWrite); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Open missing error = %v, want ErrSessionNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("Open created missing session: %v", err)
	}

	first, err := persistence.Create(CreateOptions{SessionID: "created"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())
	if _, err := persistence.Create(CreateOptions{SessionID: "created"}); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate Create error = %v, want ErrSessionExists", err)
	}
}

func TestFilesystemReadOnlyQueryWritesIndexOutsideSessionDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	persistence := NewFilesystemPersistence(root)
	writer, err := persistence.Create(CreateOptions{SessionID: "readonly"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "diagnostic", Optional: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	sessionDir := filepath.Join(root, "readonly")
	before, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := persistence.Open("readonly", ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(t.Context(), 0, 10); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("read-only query changed session directory: before=%d after=%d", len(before), len(after))
	}
	if _, err := os.Stat(filepath.Join(root, ".query-cache", "readonly", "events.offset-index.json")); err != nil {
		t.Fatalf("external query index: %v", err)
	}
}

type manualTimer struct {
	mu      sync.Mutex
	stopped bool
	fire    func()
}

func (t *manualTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (t *manualTimer) trigger() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	fire := t.fire
	t.mu.Unlock()
	fire()
}

type manualScheduler struct {
	mu        sync.Mutex
	durations []time.Duration
	timers    []*manualTimer
}

func (s *manualScheduler) after(delay time.Duration, fire func()) timerHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	timer := &manualTimer{fire: fire}
	s.durations = append(s.durations, delay)
	s.timers = append(s.timers, timer)
	return timer
}

func (s *manualScheduler) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers)
}

func (s *manualScheduler) trigger(index int) {
	s.mu.Lock()
	timer := s.timers[index]
	s.mu.Unlock()
	timer.trigger()
}

func TestAppendCommitsToMemoryBeforeDurability(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	scheduler := &manualScheduler{}
	s, err := OpenWithOptions(dir, "s", OpenOptions{AfterFunc: scheduler.after})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	commit, err := s.Append(t.Context(), Batch{OperationID: "turn-1", Events: []Event{{Kind: "turn/start"}}})
	if err != nil {
		t.Fatal(err)
	}
	if commit.FirstSequence != 1 || commit.LastSequence() != 1 {
		t.Fatalf("commit = %+v", commit)
	}
	snapshot := s.Snapshot()
	if snapshot.EventSequence != 1 || snapshot.DurableSequence != 0 || snapshot.Projection.TurnID == "" {
		t.Fatalf("live snapshot = %+v", snapshot)
	}
	commits, err := Replay(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 0 {
		t.Fatalf("cold replay observed %d unflushed commits", len(commits))
	}
	if scheduler.count() != 1 || scheduler.durations[0] != 200*time.Millisecond {
		t.Fatalf("scheduled drains = %d at %v", scheduler.count(), scheduler.durations)
	}
}

func TestPreparePublishesLargePayloadBeforeAcceptance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	scheduler := &manualScheduler{}
	s, err := OpenWithOptions(dir, "s", OpenOptions{AfterFunc: scheduler.after})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	payload := append([]byte(`{"text":"`), bytes.Repeat([]byte("x"), v4InlinePayloadBytes+1)...)
	payload = append(payload, []byte(`"}`)...)
	prepared, err := s.PrepareBatchContext(t.Context(), "large", Batch{Events: []Event{{Kind: "diagnostic", Payload: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	if s.EventSequence() != 0 {
		t.Fatal("prepare advanced accepted sequence")
	}
	if len(prepared.storedEvents) != 1 || prepared.storedEvents[0].PayloadRef == nil || len(prepared.storedEvents[0].Payload) != 0 {
		t.Fatalf("prepared storage event = %#v", prepared.storedEvents)
	}
	if err := s.contentStore().Verify(t.Context(), *prepared.storedEvents[0].PayloadRef); err != nil {
		t.Fatalf("content was not durable before acceptance: %v", err)
	}
	if _, err := s.CommitPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	s.binding.mu.Lock()
	queued := cloneCommit(s.binding.queue[0])
	s.binding.mu.Unlock()
	if len(queued.Events[0].Payload) != 0 || queued.Events[0].PayloadRef == nil {
		t.Fatalf("write queue retained large body: %#v", queued.Events[0])
	}
	if got := s.Snapshot().Projection.CommittedSequence; got != 1 {
		t.Fatalf("logical projection sequence = %d", got)
	}
}

func TestPendingHotBudgetBackpressureIsCancellable(t *testing.T) {
	binding := newPersistenceBinding(nil, t.TempDir(), 0, OpenOptions{})
	first, err := binding.reserve(t.Context(), pendingHotBytes*4)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := binding.reserve(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("backpressure error = %v, want context cancellation", err)
	}
	first.release()
	second, err := binding.reserve(t.Context(), 1)
	if err != nil {
		t.Fatalf("capacity was not returned: %v", err)
	}
	second.release()
}

func TestRejectedPersistenceAcceptanceDoesNotMutateSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	s, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	// Simulate the binding admission boundary closing immediately before a
	// prepared business batch is accepted. The Session projection and sequence
	// must remain unchanged; there is no accepted fact without a queued copy.
	prepared, err := s.PrepareBatch("one", Batch{Events: []Event{{Kind: "turn/start"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.binding.stopAccepting()
	if _, err := s.CommitPrepared(prepared); err == nil {
		t.Fatal("commit succeeded after persistence admission closed")
	}
	if snapshot := s.Snapshot(); snapshot.EventSequence != 0 || snapshot.Projection.TurnID != "" {
		t.Fatalf("rejected commit changed session state: %+v", snapshot)
	}
}

func TestBatchDeadlineIsFixedAndDoesNotReset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	scheduler := &manualScheduler{}
	s, err := OpenWithOptions(dir, "s", OpenOptions{AfterFunc: scheduler.after})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "two", Events: []Event{{Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	if scheduler.count() != 1 {
		t.Fatalf("later append reset the batch deadline: timers=%d", scheduler.count())
	}
	scheduler.trigger(0)
	if got := s.Snapshot().DurableSequence; got != 2 {
		t.Fatalf("durable sequence after scheduled drain = %d", got)
	}
}

func TestFlushDrainsWritesThatArriveDuringDrain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s, err := OpenWithOptions(dir, "s", OpenOptions{
		Write: func(ctx context.Context, w io.Writer, data []byte) error {
			once.Do(func() {
				close(started)
				<-release
			})
			return writeAllContext(ctx, w, data)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, flushErr := s.Flush(context.Background())
		done <- flushErr
	}()
	<-started
	if _, err := s.Append(t.Context(), Batch{OperationID: "two", Events: []Event{{Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().DurableSequence; got != 2 {
		t.Fatalf("flush stopped before concurrent append: durable=%d", got)
	}
}

func TestCancellingOneFlushWaiterDoesNotCancelTheSharedWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	writes := 0
	s, err := OpenWithOptions(dir, "s", OpenOptions{
		Write: func(ctx context.Context, w io.Writer, data []byte) error {
			mu.Lock()
			writes++
			if writes == 1 {
				close(started)
			}
			mu.Unlock()
			<-release
			return writeAllContext(ctx, w, data)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, flushErr := s.Flush(firstCtx)
		first <- flushErr
	}()
	<-started
	second := make(chan error, 1)
	go func() {
		_, flushErr := s.Flush(context.Background())
		second <- flushErr
	}()
	cancelFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 || s.Snapshot().DurableSequence != 1 {
		t.Fatalf("writes=%d snapshot=%+v", writes, s.Snapshot())
	}
}

func TestScheduledDrainImmediatelyConsumesWritesThatArriveDuringDrain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	scheduler := &manualScheduler{}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s, err := OpenWithOptions(dir, "s", OpenOptions{
		AfterFunc: scheduler.after,
		Write: func(ctx context.Context, w io.Writer, data []byte) error {
			once.Do(func() {
				close(started)
				<-release
			})
			return writeAllContext(ctx, w, data)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	go scheduler.trigger(0)
	<-started
	if _, err := s.Append(t.Context(), Batch{OperationID: "two", Events: []Event{{Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.After(2 * time.Second)
	for s.Snapshot().DurableSequence != 2 {
		select {
		case <-deadline:
			t.Fatalf("scheduled drain stopped before concurrent append: %+v", s.Snapshot())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if scheduler.count() != 1 {
		t.Fatalf("active drain scheduled a second batching window: %d", scheduler.count())
	}
}

func TestBackgroundFailurePausesRetryAndExplicitFlushRetries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	scheduler := &manualScheduler{}
	injected := errors.New("disk full")
	var mu sync.Mutex
	writes := 0
	s, err := OpenWithOptions(dir, "s", OpenOptions{
		AfterFunc: scheduler.after,
		Write: func(ctx context.Context, w io.Writer, data []byte) error {
			mu.Lock()
			writes++
			attempt := writes
			mu.Unlock()
			if attempt == 1 {
				return injected
			}
			return writeAllContext(ctx, w, data)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	scheduler.trigger(0)
	if snapshot := s.Snapshot(); snapshot.DurableSequence != 0 || snapshot.PersistenceStatus != PersistenceFailed {
		t.Fatalf("snapshot after background failure = %+v", snapshot)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "two", Events: []Event{{Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	if scheduler.count() != 1 {
		t.Fatalf("failed writer scheduled an automatic retry: %d timers", scheduler.count())
	}
	receipt, err := s.Flush(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.DurableSequence != 2 || s.Snapshot().PersistenceStatus != PersistenceReady {
		t.Fatalf("flush receipt/snapshot = %+v / %+v", receipt, s.Snapshot())
	}
}

func TestOperationRetryIsIdempotentAndConflictFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	s, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	one := Batch{OperationID: "same", Events: []Event{{Kind: "turn/start"}}}
	first, err := s.Append(t.Context(), one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Append(t.Context(), one)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || s.Snapshot().EventSequence != 1 {
		t.Fatalf("retry appended twice: first=%+v second=%+v snapshot=%+v", first, second, s.Snapshot())
	}
	_, err = s.Append(t.Context(), Batch{OperationID: "same", TurnID: "different-turn", Events: []Event{{Kind: "turn/start"}}})
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("same payload for a different turn error = %v", err)
	}
	_, err = s.Append(t.Context(), Batch{OperationID: "same", Events: []Event{{Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}})
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conflicting retry error = %v", err)
	}
	if _, err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.commits) != 0 {
		t.Fatalf("durable commits remained in the runtime: %d", len(s.commits))
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	if len(reopened.commits) != 0 || len(reopened.operations["same"].commit.Events) != 0 {
		t.Fatalf("reopen retained durable bodies: commits=%d operation=%+v", len(reopened.commits), reopened.operations["same"])
	}
	retried, err := reopened.Append(t.Context(), one)
	if err != nil || retried.ID != first.ID || retried.FirstSequence != first.FirstSequence || len(retried.Events) != 1 {
		t.Fatalf("reopened retry = %+v, %v", retried, err)
	}
	page, err := reopened.AcceptedPage(t.Context(), 0, 10)
	if err != nil || len(page.Commits) != 1 || page.Commits[0].ID != first.ID {
		t.Fatalf("accepted durable page = %+v, %v", page, err)
	}
}

func TestExplicitFlushRepairsPreservedPartialAppendWithoutDuplicateCommit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	injected := errors.New("connection to filesystem interrupted")
	writes := 0
	s, err := OpenWithOptions(dir, "s", OpenOptions{Write: func(ctx context.Context, w io.Writer, data []byte) error {
		writes++
		if writes == 1 {
			if _, err := w.Write(data[:len(data)/2]); err != nil {
				return err
			}
			return injected
		}
		return writeAllContext(ctx, w, data)
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flush(t.Context()); !errors.Is(err, ErrPersistenceUncertain) {
		t.Fatalf("partial append error = %v", err)
	}
	if snapshot := s.Snapshot(); snapshot.DurableSequence != 0 || snapshot.PersistenceStatus != PersistenceUncertain {
		t.Fatalf("partial append snapshot = %+v", snapshot)
	}
	receipt, err := s.Flush(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.DurableSequence != 1 || writes != 2 {
		t.Fatalf("repair receipt=%+v writes=%d", receipt, writes)
	}
	commits, err := Replay(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].OperationID != "one" {
		t.Fatalf("repaired commits = %+v", commits)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "events.uncertain-*.tail"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("partial tail backups=%v err=%v", backups, err)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Size() == 0 {
		t.Fatalf("partial tail backup info=%v err=%v", info, err)
	}
}

func TestExplicitFlushRecognizesCompleteAppendAfterWriteError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	writes := 0
	s, err := OpenWithOptions(dir, "s", OpenOptions{Write: func(ctx context.Context, w io.Writer, data []byte) error {
		writes++
		if err := writeAllContext(ctx, w, data); err != nil {
			return err
		}
		if writes == 1 {
			return errors.New("late write acknowledgement lost")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	// A full exact record is proved and synced during the first call, so the
	// write implementation's late error is treated as success immediately.
	if receipt, err := s.Flush(t.Context()); err != nil || receipt.DurableSequence != 1 {
		t.Fatalf("flush receipt=%+v err=%v", receipt, err)
	}
	if writes != 1 {
		t.Fatalf("complete uncertain append was written %d times", writes)
	}
	commits, err := Replay(dir, nil)
	if err != nil || len(commits) != 1 {
		t.Fatalf("commits=%+v err=%v", commits, err)
	}
}

func TestExplicitFlushRetriesOnlySyncAfterUncertainFsync(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	writes, syncs := 0, 0
	s, err := OpenWithOptions(dir, "s", OpenOptions{
		Write: func(ctx context.Context, w io.Writer, data []byte) error {
			writes++
			return writeAllContext(ctx, w, data)
		},
		Sync: func(file *os.File) error {
			syncs++
			if syncs == 1 {
				return errors.New("fsync interrupted")
			}
			return file.Sync()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flush(t.Context()); !errors.Is(err, ErrPersistenceUncertain) {
		t.Fatalf("first fsync error = %v", err)
	}
	if receipt, err := s.Flush(t.Context()); err != nil || receipt.DurableSequence != 1 {
		t.Fatalf("retry receipt=%+v err=%v", receipt, err)
	}
	if writes != 1 || syncs != 2 {
		t.Fatalf("writes=%d syncs=%d, want 1/2", writes, syncs)
	}
}

func TestConfirmedUncertainPrefixDoesNotMarkLaterBatchDurable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	writes, syncs := 0, 0
	s, err := OpenWithOptions(dir, "s", OpenOptions{
		Write: func(ctx context.Context, w io.Writer, data []byte) error {
			writes++
			if err := writeAllContext(ctx, w, data); err != nil {
				return err
			}
			if writes == 1 {
				return errors.New("append acknowledgement lost")
			}
			return nil
		},
		Sync: func(file *os.File) error {
			syncs++
			if syncs == 1 {
				return errors.New("first sync unavailable")
			}
			return file.Sync()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Append(t.Context(), Batch{OperationID: "a", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flush(t.Context()); !errors.Is(err, ErrPersistenceUncertain) {
		t.Fatalf("first flush error = %v", err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "b", Events: []Event{{Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.Flush(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.DurableSequence != 2 || writes != 2 {
		t.Fatalf("receipt=%+v writes=%d syncs=%d", receipt, writes, syncs)
	}
	commits, err := Replay(dir, nil)
	if err != nil || len(commits) != 2 || commits[0].OperationID != "a" || commits[1].OperationID != "b" {
		t.Fatalf("commits=%+v err=%v", commits, err)
	}
}

func TestCloseReturnsStableFailureAndReleasesOwnership(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	injected := errors.New("disk unavailable")
	s, err := OpenWithOptions(dir, "s", OpenOptions{Write: func(context.Context, io.Writer, []byte) error {
		return injected
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	first := s.Close(t.Context())
	second := s.Close(context.Background())
	if !errors.Is(first, injected) || first.Error() != second.Error() {
		t.Fatalf("close errors first=%v second=%v", first, second)
	}
	if reopened, err := Open(dir, "s"); err != nil {
		t.Fatalf("close did not release writer ownership: %v", err)
	} else {
		_ = reopened.Close(context.Background())
	}
}
