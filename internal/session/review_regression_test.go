package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"reasonix/internal/filelock"
	"reasonix/internal/provider"
)

// owner grants host authority for an exact instance. Tests use it wherever a
// host would retire a runtime it published itself.
func owner(t *testing.T, service *Service, runtime *Runtime) *RuntimeOwner {
	t.Helper()
	grant, err := service.Owner(runtime)
	if err != nil {
		t.Fatalf("owner grant: %v", err)
	}
	return grant
}

func reviewRuntime(t *testing.T) (*Service, *Runtime) {
	t.Helper()
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "review"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.close(context.Background()) })
	return service, runtime
}

func TestStateSnapshotOmitsHistory(t *testing.T) {
	_, runtime := reviewRuntime(t)
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: "visible", Role: provider.RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), "message", []Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	state := runtime.StateSnapshot().Session
	if state.EventSequence != 1 || len(state.Projection.Messages) != 0 || len(state.Projection.ModelMessages) != 0 {
		t.Fatalf("activity snapshot includes history or loses its sequence: %+v", state)
	}
	if len(runtime.Snapshot().Session.Projection.Messages) != 1 {
		t.Fatal("state snapshot changed the stored history")
	}
}

func TestSessionIdentityRejectsPathsWithoutCreatingFiles(t *testing.T) {
	persistence := NewFilesystemPersistence(t.TempDir())
	for _, id := range []string{"..", "../escape", "a/b", `a\b`, "/absolute", ".hidden", "CON", "com1.log", "bad:name", ".", ""} {
		if _, err := persistence.Open(id, ReadWrite); err == nil {
			t.Fatalf("accepted path as identity: %q", id)
		}
	}
	entries, err := os.ReadDir(persistence.Root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid opens changed the root: %v, %v", entries, err)
	}
}

func TestSessionIdentityRejectsSymlinkOutsideRoot(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	store, err := CreateStore(outside, "escape")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "sessions-v4")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	persistence := NewFilesystemPersistence(root)
	if _, err := persistence.Stat(t.Context(), "escape"); err == nil {
		t.Fatal("metadata lookup followed a session symlink outside the store root")
	}
	if _, err := persistence.Open("escape", ReadOnly); err == nil {
		t.Fatal("read-only open followed a session symlink outside the store root")
	}
	if err := persistence.Delete(t.Context(), "escape"); err == nil {
		t.Fatal("delete followed a session symlink outside the store root")
	}
}

func TestDirectoryOwnershipExcludesWriterDuringRename(t *testing.T) {
	_, runtime := reviewRuntime(t)
	store := runtime.Session().Handle().(*Store)
	if err := runtime.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	release, err := filelock.Acquire(t.Context(), directoryOwnershipPath(store.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if reopened, err := Open(store.dir, store.SessionID()); !errors.Is(err, ErrWriterOwned) {
		if reopened != nil {
			_ = reopened.Close(t.Context())
		}
		t.Fatalf("writer entered the directory transition: %v", err)
	}
	// The ownership file stays outside the moved tree, including on Windows.
	if err := os.Rename(store.dir, store.dir+"-removed"); err != nil {
		t.Fatalf("directory ownership prevents rename: %v", err)
	}
}

func TestCancelReceiptDoesNotWaitForSessionProjection(t *testing.T) {
	service, runtime := reviewRuntime(t)
	ctx, _ := bindTestExecution(t, runtime, "model")
	store := runtime.Session().Handle().(*Store)
	store.mu.Lock()
	defer store.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := service.CancelSession(runtime.Ref()); done <- err }()
	select {
	case err := <-done:
		if err != nil || !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("cancel = %v, context = %v", err, ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel receipt waits for the projection lock")
	}
}

func TestCancelSignalsWithoutRuntimeMutex(t *testing.T) {
	service, runtime := reviewRuntime(t)
	ctx, _ := bindTestExecution(t, runtime, "model")

	runtime.mu.Lock()
	done := make(chan CancelReceipt, 1)
	go func() {
		receipt, cancelErr := service.CancelSession(runtime.Ref())
		if cancelErr != nil {
			t.Errorf("cancel: %v", cancelErr)
		}
		done <- receipt
	}()
	select {
	case receipt := <-done:
		if !receipt.Accepted || receipt.Phase != RuntimeCancelling {
			t.Fatalf("receipt = %+v", receipt)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("activity context = %v", ctx.Err())
		}
	case <-time.After(5 * time.Second):
		runtime.mu.Unlock()
		t.Fatal("cancel waited for the runtime commit mutex")
	}
	runtime.mu.Unlock()
}

func TestUnknownRequiredPrefixDoesNotTruncateTail(t *testing.T) {
	_, runtime := reviewRuntime(t)
	store := runtime.Session().Handle().(*Store)
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "known", Events: []Event{{Kind: "diagnostic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, currentLogName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unknown := Commit{
		SchemaVersion: SchemaVersion, Codec: Codec, RecordType: "commit", ID: "future-commit",
		OperationID: "future-operation", OperationHash: "future-hash", FirstSequence: 2,
		EventCount: 1, WriterGeneration: store.Manifest().WriterGeneration, CreatedAt: time.Now().UTC(),
		Events: []Event{{ID: "future-event", Sequence: 2, Kind: "future/required"}},
	}
	var encoded bytes.Buffer
	if _, err := encodeV4Commits(t.Context(), &encoded, contentStoreForSessionDir(store.dir), []Commit{unknown}); err != nil {
		t.Fatal(err)
	}
	data = append(data, encoded.Bytes()...)
	data = append(data, []byte(`{"torn":`)...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(store.dir, store.SessionID()); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("open = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatalf("unsupported log was changed: %v", err)
	}
}

func TestSnapshotCannotMutateAcceptedMessageMetadata(t *testing.T) {
	_, runtime := reviewRuntime(t)
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: "m1", Role: provider.RoleUser, Images: []string{"original"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), "input", []Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Session().Snapshot()
	snapshot.Projection.Messages[0].Images[0] = "changed"
	snapshot.Projection.ModelMessages[0].Images[0] = "changed-again"
	if got := runtime.Session().Snapshot().Projection.Messages[0].Images[0]; got != "original" {
		t.Fatalf("observer mutated accepted message: %q", got)
	}
}

func TestOldRuntimeDisposerCannotCloseSuccessor(t *testing.T) {
	service, old := reviewRuntime(t)
	oldOwner := owner(t, service, old)
	if err := oldOwner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	binding, err := service.Open(t.Context(), old.Ref())
	if err != nil {
		t.Fatal(err)
	}
	next := binding.Runtime()
	t.Cleanup(func() { _ = binding.Release(context.Background()) })
	// The delayed disposer still holds its own grant for the retired instance.
	if err := oldOwner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, ok := service.Runtime(next.Ref()); !ok || got != next {
		t.Fatal("old disposer removed successor")
	}
	if _, err := next.Session().AppendBatch(t.Context(), "still-open", []Event{{Kind: "diagnostic", Optional: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestClientBindingOwnsDetachButNotRuntimeClose(t *testing.T) {
	service, runtime := reviewRuntime(t)
	service.idleTTL = 20 * time.Millisecond
	first, err := service.Bind(runtime)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Bind(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner(t, service, runtime).Close(t.Context()); !errors.Is(err, ErrRuntimeBound) {
		t.Fatalf("close bound runtime = %v", err)
	}
	if err := first.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, ok := service.Runtime(runtime.Ref()); !ok || got != runtime {
		t.Fatal("one client detached the runtime used by another client")
	}
	if err := second.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, ok := service.Runtime(runtime.Ref()); !ok || got != runtime {
		t.Fatal("idle runtime was not retained for quick rebinding")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := service.Runtime(runtime.Ref()); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("idle runtime remained published after its retention period")
}

func TestIdleRuntimeCacheBudgetRetiresLeastRecentlyUsedRuntime(t *testing.T) {
	service, first := reviewRuntime(t)
	service.idleTTL = time.Hour
	service.idleBudget = 64 << 10
	firstBinding, err := service.Bind(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(t.Context(), CreateOptions{SessionID: "budget-second"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, ref := range []SessionRef{first.Ref(), second.Ref()} {
			if _, live := service.Runtime(ref); live {
				_ = service.Close(context.Background(), ref)
			}
		}
	})
	secondBinding, err := service.Bind(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstBinding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := secondBinding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, firstLive := service.Runtime(first.Ref())
		_, secondLive := service.Runtime(second.Ref())
		if !firstLive && secondLive {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("idle cache budget did not retire the least recently used runtime")
}

func TestLastClientDetachDoesNotCancelActiveRuntime(t *testing.T) {
	service, runtime := reviewRuntime(t)
	service.idleTTL = 20 * time.Millisecond
	binding, err := service.Bind(runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, exec := bindTestExecution(t, runtime, "model")
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("client detach cancelled host-owned activity: %v", ctx.Err())
	}
	if got, ok := service.Runtime(runtime.Ref()); !ok || got != runtime {
		t.Fatal("active runtime retired when its last client detached")
	}
	exec.Finish()
	if _, ok := service.Runtime(runtime.Ref()); !ok {
		t.Fatal("completed runtime was not retained for quick rebinding")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := service.Runtime(runtime.Ref()); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("unbound runtime did not retire after its retention period")
}

func TestOwnerCloseFencesConcurrentClientBinding(t *testing.T) {
	service, sessionRuntime := reviewRuntime(t)
	sessionRuntime.mu.Lock()
	closed := make(chan error, 1)
	grant := owner(t, service, sessionRuntime)
	go func() { closed <- grant.Close(t.Context()) }()
	deadline := time.After(5 * time.Second)
	for {
		service.mu.Lock()
		retiring := service.retiring[sessionRuntime] != nil
		service.mu.Unlock()
		if retiring {
			break
		}
		select {
		case <-deadline:
			sessionRuntime.mu.Unlock()
			t.Fatal("owner close did not publish its retirement fence")
		default:
			runtime.Gosched()
		}
	}
	if _, err := service.Bind(sessionRuntime); !errors.Is(err, ErrRuntimeRetiring) {
		sessionRuntime.mu.Unlock()
		t.Fatalf("bind during owner close = %v", err)
	}
	sessionRuntime.mu.Unlock()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestTitleCanReturnToEarlierValue(t *testing.T) {
	service, runtime := reviewRuntime(t)
	for _, title := range []string{"A", "B", "A"} {
		if err := service.SetTitle(t.Context(), runtime.Ref(), title); err != nil {
			t.Fatal(err)
		}
		if got := runtime.Session().Snapshot().Projection.Title; got != title {
			t.Fatalf("title = %q, want %q", got, title)
		}
	}
}

func TestCloseFailureUnregistersReleasedWriter(t *testing.T) {
	service, runtime := reviewRuntime(t)
	store := runtime.Session().Handle().(*Store)
	failure := errors.New("disk unavailable")
	store.writeFn = func(context.Context, io.Writer, []byte) error { return failure }
	if _, err := runtime.Session().AppendBatch(t.Context(), "pending", []Event{{Kind: "diagnostic", Optional: true}}); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), runtime.Ref()); !errors.Is(err, failure) {
		t.Fatalf("close = %v", err)
	}
	if _, ok := service.Runtime(runtime.Ref()); ok {
		t.Fatal("closed writer remains attachable")
	}
	if err := service.Close(t.Context(), runtime.Ref()); !errors.Is(err, failure) {
		t.Fatalf("repeat close = %v", err)
	}
	binding, err := service.Open(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Release(context.Background()) })
	if binding.Runtime() == runtime {
		t.Fatal("open returned released writer")
	}
}

func TestRewindBeforeFirstTurnPreservesInitialization(t *testing.T) {
	service, runtime := reviewRuntime(t)
	if _, err := runtime.Session().AppendBatch(t.Context(), "config", []Event{{Kind: "session/config", Payload: []byte(`{"modelRef":"test/model"}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "input", TurnID: "first", Events: []Event{{Kind: "turn/start"}, {Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	child, err := service.Rewind(t.Context(), runtime.Ref(), "first", "rewound")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), child.Ref()) })
	if got := child.Session().Snapshot().Projection.ModelRef; got != "test/model" {
		t.Fatalf("model = %q", got)
	}
}

func TestFinishedActivityCancelsItsContext(t *testing.T) {
	_, runtime := reviewRuntime(t)
	ctx, exec := bindTestExecution(t, runtime, "model")
	exec.cancel()
	exec.Finish()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("finished activity retains live cancellation context")
	}
}

func TestForkCopiesOwnedAttachments(t *testing.T) {
	service, runtime := reviewRuntime(t)
	dir := runtime.Session().Handle().(*Store).dir
	asset := filepath.Join(dir, "attachments", "input.txt")
	if err := os.MkdirAll(filepath.Dir(asset), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("owned context"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "input", TurnID: "first", Events: []Event{{Kind: "turn/start"}, {Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	child, err := service.Fork(t.Context(), runtime.Ref(), "first", "child")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), child.Ref()) })
	if err := service.Delete(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(child.Session().Handle().(*Store).dir, "attachments", "input.txt"))
	if err != nil || string(data) != "owned context" {
		t.Fatalf("child attachment = %q, %v", data, err)
	}
}
