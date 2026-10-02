package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	filelock "reasonix/internal/identitylock"
)

func directoryOwnershipPath(dir string) string {
	return filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".ownership.lock")
}

// writerOwnership is this process's view of the writer leases it holds. The
// probe answers from it without touching the lock file, and its mutex makes an
// in-process probe and an in-process acquire mutually exclusive: the probe's
// transient exclusive hold can therefore never be what an acquirer collides
// with inside this process.
var writerOwnership = struct {
	mu   sync.Mutex
	held map[string]int
}{held: map[string]int{}}

// A probe in another process holds writer.lock for microseconds. An acquirer
// that lands in that window sees ErrHeld exactly as it would for a real owner,
// so it retries a bounded number of times before reporting the lock as owned.
// The bound is small enough that a genuine owner is still reported promptly.
const writerProbeCollisionRetries = 5

var writerProbeCollisionBackoff = 10 * time.Millisecond

// Test seams for the collision protocol: the probe hook runs while the probe
// holds the transient lock and the ownership mutex, the acquire hooks run
// before the acquirer first contends for that mutex and after each ErrHeld it
// decides to retry.
var (
	writerProbeHoldHookForTest    func()
	writerAcquireEnterHookForTest func()
	writerAcquireRetryHookForTest func(attempt int)
)

func writerOwnershipKey(dir string) string {
	dir = strings.TrimSpace(dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return filepath.Clean(abs)
}

func writerHeldLocally(key string) bool {
	writerOwnership.mu.Lock()
	defer writerOwnership.mu.Unlock()
	return writerOwnership.held[key] > 0
}

// Keep both claims for a writer's lifetime: the inner claim excludes existing
// writers and the outer claim remains usable while the directory is moved.
func acquireSessionWriter(dir string) (func(), error) {
	if writerAcquireEnterHookForTest != nil {
		writerAcquireEnterHookForTest()
	}
	key := writerOwnershipKey(dir)
	if writerHeldLocally(key) {
		return nil, filelock.ErrHeld
	}
	releaseDirectory, err := filelock.TryAcquire(directoryOwnershipPath(dir))
	if err != nil {
		return nil, err
	}
	releaseWriter, err := acquireWriterLock(dir, key)
	if err != nil {
		releaseDirectory()
		return nil, err
	}
	return func() {
		releaseWriter()
		releaseDirectory()
	}, nil
}

// acquireWriterLock takes writer.lock under the ownership mutex and records the
// hold. Only the inner lock is retried: the probe never touches the directory
// claim, so ErrHeld there is always a real owner.
func acquireWriterLock(dir, key string) (func(), error) {
	lockPath := filepath.Join(dir, "writer.lock")
	for attempt := 0; ; attempt++ {
		writerOwnership.mu.Lock()
		release, err := filelock.TryAcquire(lockPath)
		if err == nil {
			writerOwnership.held[key]++
			writerOwnership.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					writerOwnership.mu.Lock()
					release()
					if writerOwnership.held[key] <= 1 {
						delete(writerOwnership.held, key)
					} else {
						writerOwnership.held[key]--
					}
					writerOwnership.mu.Unlock()
				})
			}, nil
		}
		writerOwnership.mu.Unlock()
		if !errors.Is(err, filelock.ErrHeld) || attempt >= writerProbeCollisionRetries {
			return nil, err
		}
		if writerAcquireRetryHookForTest != nil {
			writerAcquireRetryHookForTest(attempt)
		}
		time.Sleep(writerProbeCollisionBackoff)
	}
}

// ProbeWriterHeld reports whether some runtime currently owns the session's
// writer lease. It is the occupancy oracle for final-format identities: the
// takeover protocol asks the holder to stand down and then watches this probe
// turn false before re-acquiring. A lease this process holds is answered from
// the ownership registry; only a foreign hold needs the lock file, and a
// session whose writer.lock was never created counts as free without creating
// it. A missing session directory counts as free.
func ProbeWriterHeld(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	if writerHeldLocally(writerOwnershipKey(dir)) {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		return false
	}
	lockPath := filepath.Join(dir, "writer.lock")
	if _, err := os.Stat(lockPath); err != nil {
		return false
	}
	writerOwnership.mu.Lock()
	defer writerOwnership.mu.Unlock()
	release, err := filelock.TryAcquire(lockPath)
	if err != nil {
		return true
	}
	if writerProbeHoldHookForTest != nil {
		writerProbeHoldHookForTest()
	}
	release()
	return false
}
