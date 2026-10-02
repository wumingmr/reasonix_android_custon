package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/desktop/internal/sessionui"
	"reasonix/internal/identitylock"
	"reasonix/internal/session"
)

func seedManualCreation(t *testing.T, a *App, id, phase string) (ManualSessionCreationView, sessionui.Record) {
	t.Helper()
	workspace, err := a.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(id))
	v := ManualSessionCreationView{OperationID: id, WorkspaceID: workspace, Scope: "global", Phase: phase,
		Ref:     session.SessionRef{HostID: "local", SessionID: fmt.Sprintf("desktop-manual-%x", sum[:16])},
		TopicID: fmt.Sprintf("manual-%x", sum[:16]), Settings: a.defaultDraftSettings("global", "")}
	b, _ := json.Marshal(v)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	fields["future"] = json.RawMessage(`{"preserve":true}`)
	b, _ = json.Marshal(fields)
	r, err := a.sessionUIStore().Save(t.Context(), "creation", id, "0", b)
	if err != nil {
		t.Fatal(err)
	}
	return v, r
}

// A real second process is required: zero-byte file existence is not ownership.
func TestManualCreationLockProcess(t *testing.T) {
	path := os.Getenv("REASONIX_TEST_CREATION_LOCK")
	if path == "" {
		return
	}
	release, err := identitylock.TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func holdManualCreationInProcess(t *testing.T, a *App, v ManualSessionCreationView) func() {
	t.Helper()
	dir := filepath.Join(filepath.Dir(a.sessionUIStore().Path()), "manual-creation-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestManualCreationLockProcess$")
	cmd.Env = append(os.Environ(), "REASONIX_TEST_CREATION_LOCK="+filepath.Join(dir, v.Ref.SessionID+".lock"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("child lock handshake: %q %v", line, err)
	}
	return func() {
		_ = in.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManualCreationRecoversAfterOtherProcessReleasesLock(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, before := seedManualCreation(t, a, "recovery-after-owner-exit", "starting")
	release := holdManualCreationInProcess(t, a, v)
	a.markTabsRestored()
	a.reconcileManualSessionCreations()
	m := a.creationManager()
	waitFor(t, "observed process lock", func() bool { p := m.Snapshot(v.OperationID); return p != nil && p.Status == "waiting_lock" })
	current, err := a.sessionUIStore().Get(t.Context(), "creation", v.OperationID)
	if err != nil || current.Revision != before.Revision {
		t.Fatal("waiting mutated the record", err)
	}
	// A former owner may have written another starting revision during the wait.
	_, err = a.sessionUIStore().Save(t.Context(), "creation", v.OperationID, current.Revision, current.Payload)
	if err != nil {
		t.Fatal(err)
	}
	release()
	a.manualCreationTasks.Wait()
	got, err := a.GetManualSessionCreation(v.OperationID)
	if err != nil || got.Phase != "ready" || got.Ref != v.Ref {
		t.Fatalf("automatic recovery: %+v %v", got, err)
	}
	stored, _ := a.sessionUIStore().Get(t.Context(), "creation", v.OperationID)
	if !strings.Contains(string(stored.Payload), `"future":{"preserve":true}`) || strings.Contains(string(stored.Payload), `"progress"`) {
		t.Fatalf("payload compatibility: %s", stored.Payload)
	}
}

type creationStorageFault struct {
	manualCreationStore
	failures atomic.Int32
	saves    atomic.Int32
	err      error
}

type creationBusyError struct{}

func (creationBusyError) Error() string { return "injected busy" }
func (creationBusyError) Code() int     { return 5 }

func (s *creationStorageFault) Save(ctx context.Context, kind, key, expected string, payload json.RawMessage, submissions ...sessionui.Record) (sessionui.Record, error) {
	var v ManualSessionCreationView
	_ = json.Unmarshal(payload, &v)
	if v.Phase == "ready" {
		s.saves.Add(1)
		if s.failures.Add(-1) >= 0 {
			return sessionui.Record{}, s.err
		}
	}
	return s.manualCreationStore.Save(ctx, kind, key, expected, payload, submissions...)
}

func TestManualCreationResultRetryDoesNotRebuild(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "creation-result-retry", "starting")
	m := a.creationManager()
	fault := &creationStorageFault{manualCreationStore: m.store, err: creationBusyError{}}
	fault.failures.Store(2)
	m.store = fault
	var builds atomic.Int32
	m.execute = func(context.Context, ManualSessionCreationView, func(string)) error { builds.Add(1); return nil }
	m.Ensure(v.OperationID, "recovery", "")
	waitFor(t, "saving retry", func() bool { p := m.Snapshot(v.OperationID); return p != nil && p.Status == "retrying_storage" })
	for range 20 {
		m.Ensure(v.OperationID, "retry", "1")
	}
	a.manualCreationTasks.Wait()
	if builds.Load() != 1 || fault.saves.Load() != 3 {
		t.Fatalf("builds=%d saves=%d", builds.Load(), fault.saves.Load())
	}
	got, _ := a.GetManualSessionCreation(v.OperationID)
	if got.Phase != "ready" {
		t.Fatalf("result=%+v", got)
	}
}

func TestManualCreationShutdownDoesNotReleaseRunningOwner(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "creation-blocked-exit", "starting")
	m := a.creationManager()
	entered, release := make(chan struct{}), make(chan struct{})
	m.execute = func(context.Context, ManualSessionCreationView, func(string)) error {
		close(entered)
		<-release
		return nil
	}
	m.Ensure(v.OperationID, "recovery", "")
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.CancelAndWait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop=%v", err)
	}
	path := filepath.Join(filepath.Dir(a.sessionUIStore().Path()), "manual-creation-locks", v.Ref.SessionID+".lock")
	unlock, err := identitylock.TryAcquire(path)
	if err == nil {
		unlock()
		t.Fatal("still-running owner released its lock")
	}
	if !errors.Is(err, identitylock.ErrHeld) {
		t.Fatal(err)
	}
	if _, err := a.sessionUIStore().Get(t.Context(), "creation", v.OperationID); err != nil {
		t.Fatal("store was closed", err)
	}
	close(release)
	if err := m.CancelAndWait(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := a.GetManualSessionCreation(v.OperationID)
	if got.Phase != "starting" {
		t.Fatalf("interruption lost recoverability: %+v", got)
	}
}

func TestManualCreationFutureSchemaBlocksWithoutRetryLoop(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "creation-future-schema", "starting")
	m := a.creationManager()
	fault := &creationStorageFault{manualCreationStore: m.store, err: sessionui.ErrFutureVersion}
	fault.failures.Store(100)
	m.store = fault
	m.execute = func(context.Context, ManualSessionCreationView, func(string)) error { return nil }
	m.Ensure(v.OperationID, "recovery", "")
	a.manualCreationTasks.Wait()
	p := m.Snapshot(v.OperationID)
	if p == nil || p.Status != "blocked" || p.ErrorCode != "unsupported_ui_schema" {
		t.Fatalf("progress=%+v", p)
	}
	m.Ensure(v.OperationID, "recovery", "")
	if fault.saves.Load() != 1 {
		t.Fatal("fatal storage error retried automatically")
	}
	data, err := json.Marshal(a.manualCreationDiagnosticReport())
	if err != nil || !strings.Contains(string(data), "unsupported_ui_schema") || strings.Contains(string(data), "settings") {
		t.Fatalf("diagnostics=%s %v", data, err)
	}
}
