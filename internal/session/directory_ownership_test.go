package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	filelock "reasonix/internal/identitylock"
)

func writerProbeFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "identity")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func resetWriterHooks(t *testing.T) {
	t.Helper()
	backoff := writerProbeCollisionBackoff
	t.Cleanup(func() {
		writerProbeHoldHookForTest = nil
		writerAcquireEnterHookForTest = nil
		writerAcquireRetryHookForTest = nil
		writerProbeCollisionBackoff = backoff
	})
}

// A lease this process holds is answered from the ownership registry: the
// probe must not take the lock file at all, so it cannot race the holder's own
// re-acquire or any other in-process acquirer.
func TestProbeWriterHeldAnswersFromInProcessOwnership(t *testing.T) {
	resetWriterHooks(t)
	dir := writerProbeFixture(t)
	writerProbeHoldHookForTest = func() { t.Fatal("probe touched the lock file for a lease this process holds") }
	release, err := acquireSessionWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ProbeWriterHeld(dir) {
		t.Fatal("probe reported an in-process writer lease as free")
	}
	release()
	writerProbeHoldHookForTest = nil
	if ProbeWriterHeld(dir) {
		t.Fatal("probe still reports the released lease as held")
	}
}

// Probing a session no writer ever opened must not manufacture writer.lock.
func TestProbeWriterHeldDoesNotCreateLockFile(t *testing.T) {
	dir := writerProbeFixture(t)
	if ProbeWriterHeld(dir) {
		t.Fatal("never-opened session reported as held")
	}
	if _, err := os.Stat(filepath.Join(dir, "writer.lock")); !os.IsNotExist(err) {
		t.Fatalf("probe created writer.lock: %v", err)
	}
}

// An in-process acquirer and an in-process probe are mutually exclusive: an
// acquire that arrives while the probe holds its transient lock waits for the
// probe to leave and then succeeds, instead of failing with ErrHeld against a
// holder that was never a writer.
func TestAcquireSessionWriterNeverCollidesWithInProcessProbe(t *testing.T) {
	resetWriterHooks(t)
	dir := writerProbeFixture(t)
	// Materialize writer.lock so the probe reaches the lock path.
	if release, err := acquireSessionWriter(dir); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	probeInside := make(chan struct{})
	releaseProbe := make(chan struct{})
	writerProbeHoldHookForTest = func() {
		close(probeInside)
		<-releaseProbe
	}
	acquirerContending := make(chan struct{})
	writerAcquireEnterHookForTest = func() { close(acquirerContending) }
	retries := 0
	writerAcquireRetryHookForTest = func(int) { retries++ }

	probeResult := make(chan bool, 1)
	go func() { probeResult <- ProbeWriterHeld(dir) }()
	<-probeInside

	type acquired struct {
		release func()
		err     error
	}
	acquireResult := make(chan acquired, 1)
	go func() {
		release, err := acquireSessionWriter(dir)
		acquireResult <- acquired{release: release, err: err}
	}()
	// The acquirer is about to contend while the probe still holds the lock;
	// only now does the probe leave. Any ErrHeld from here on would be the
	// collision this protocol rules out.
	<-acquirerContending
	close(releaseProbe)

	if held := <-probeResult; held {
		t.Fatal("probe reported a free session as held")
	}
	result := <-acquireResult
	if result.err != nil {
		t.Fatalf("acquire collided with the in-process probe: %v", result.err)
	}
	result.release()
	if retries != 0 {
		t.Fatalf("acquire needed %d retries against an in-process probe; the mutex must make that impossible", retries)
	}
}

// A probe in another process holds writer.lock for an instant. Simulate it
// with a bare identity lock (invisible to the ownership registry) that is
// released after the acquirer's first failed attempt: the acquirer retries and
// succeeds instead of reporting the session as owned.
func TestAcquireSessionWriterRetriesForeignProbeCollision(t *testing.T) {
	resetWriterHooks(t)
	writerProbeCollisionBackoff = time.Millisecond
	dir := writerProbeFixture(t)
	foreign, err := filelock.TryAcquire(filepath.Join(dir, "writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	writerAcquireRetryHookForTest = func(attempt int) {
		attempts++
		if attempt == 0 {
			foreign()
		}
	}
	release, err := acquireSessionWriter(dir)
	if err != nil {
		t.Fatalf("acquire failed against a transient foreign hold: %v", err)
	}
	release()
	if attempts != 1 {
		t.Fatalf("acquire retried %d times, want exactly 1 after the transient hold cleared", attempts)
	}
}

// A real foreign owner is still refused, after exactly the documented number
// of retries, so a genuine ErrWriterOwned stays prompt and bounded.
func TestAcquireSessionWriterRefusesForeignOwnerAfterBoundedRetries(t *testing.T) {
	resetWriterHooks(t)
	writerProbeCollisionBackoff = time.Millisecond
	dir := writerProbeFixture(t)
	foreign, err := filelock.TryAcquire(filepath.Join(dir, "writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer foreign()
	attempts := 0
	writerAcquireRetryHookForTest = func(int) { attempts++ }
	if _, err := acquireSessionWriter(dir); !errors.Is(err, filelock.ErrHeld) {
		t.Fatalf("acquire against a live foreign owner = %v, want ErrHeld", err)
	}
	if attempts != writerProbeCollisionRetries {
		t.Fatalf("acquire retried %d times, want %d", attempts, writerProbeCollisionRetries)
	}
	if !ProbeWriterHeld(dir) {
		t.Fatal("probe reported a foreign-held session as free")
	}
}
