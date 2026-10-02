// Package filelock provides bounded, cross-process advisory file locks.
package filelock

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const retryInterval = 20 * time.Millisecond

// ErrHeld reports that another file descriptor currently owns the lock.
// Callers normally see their context error after Acquire's bounded retry loop.
var ErrHeld = errors.New("file lock held")

// localLock is a process-local reader-writer lock for one canonical path.
// refs counts acquirers currently between registry entry and release/timeout
// so the registry can reclaim entries when no one is waiting or holding —
// important for short-lived paths such as session-temp owner locks.
type localLock struct {
	mu             sync.Mutex
	cond           *sync.Cond
	exclusive      bool
	readers        int
	waitingWriters int
	refs           int
}

var localRegistry = struct {
	sync.Mutex
	locks map[string]*localLock
}{locks: map[string]*localLock{}}

// Acquire obtains an exclusive lock on path until the returned release
// function is called. It serializes both goroutines in this process and other
// Reasonix processes, and never waits past ctx's deadline.
func Acquire(ctx context.Context, path string) (func(), error) {
	return acquire(ctx, path, 0, ModeExclusive)
}

// AcquireMode obtains a lock in exclusive or shared mode.
func AcquireMode(ctx context.Context, path string, mode Mode) (func(), error) {
	return acquire(ctx, path, 0, mode)
}

// AcquireModeWithKey uses localKey for the process-local queue while opening
// path for the cross-process lock. Callers with filesystem identity knowledge
// use this to make aliases share a queue without using a comparison key for IO.
func AcquireModeWithKey(ctx context.Context, path, localKey string, mode Mode) (func(), error) {
	if localKey == "" {
		return nil, errors.New("file lock local key is empty")
	}
	return acquireWithKey(ctx, path, localKey, 0, mode)
}

// AcquireWithExternalTimeout obtains an exclusive lock while keeping the
// in-process queue and cross-process file-lock budgets separate. ctx bounds
// only the wait for another goroutine in this process; externalTimeout starts
// after that queue is acquired and bounds retries against other processes.
func AcquireWithExternalTimeout(ctx context.Context, path string, externalTimeout time.Duration) (func(), error) {
	if externalTimeout <= 0 {
		return nil, errors.New("external file lock timeout must be positive")
	}
	return acquire(ctx, path, externalTimeout, ModeExclusive)
}

// AcquireWithExternalTimeoutAndKey is AcquireWithExternalTimeout with an
// explicit process-local identity key.
func AcquireWithExternalTimeoutAndKey(ctx context.Context, path, localKey string, externalTimeout time.Duration) (func(), error) {
	if externalTimeout <= 0 {
		return nil, errors.New("external file lock timeout must be positive")
	}
	if localKey == "" {
		return nil, errors.New("file lock local key is empty")
	}
	return acquireWithKey(ctx, path, localKey, externalTimeout, ModeExclusive)
}

func acquire(ctx context.Context, path string, externalTimeout time.Duration, mode Mode) (func(), error) {
	return acquireWithKey(ctx, path, "", externalTimeout, mode)
}

func acquireWithKey(ctx context.Context, path, localKey string, externalTimeout time.Duration, mode Mode) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lockPath, err := canonicalLockPath(path)
	if err != nil {
		return nil, err
	}
	key := localKey
	if key == "" {
		key = lockPath
	}
	releaseLocal, err := acquireLocal(ctx, key, mode)
	if err != nil {
		return nil, err
	}
	fileCtx := ctx
	cancel := func() {}
	if externalTimeout > 0 {
		fileCtx, cancel = context.WithTimeout(context.Background(), externalTimeout)
	}
	defer cancel()

	for {
		releaseFile, err := tryLockFileMode(lockPath, mode)
		if err == nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					releaseFile()
					releaseLocal()
				})
			}, nil
		}
		if !errors.Is(err, ErrHeld) {
			releaseLocal()
			return nil, fmt.Errorf("acquire file lock: %w", err)
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-timer.C:
		case <-fileCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			releaseLocal()
			return nil, fmt.Errorf("acquire file lock: %w", fileCtx.Err())
		}
	}
}

// TryAcquire attempts a non-blocking exclusive lock. It returns ErrHeld when
// another holder (in this process or another) currently owns the lock.
func TryAcquire(path string) (func(), error) {
	return TryAcquireMode(path, ModeExclusive)
}

// TryAcquireMode attempts a non-blocking lock in exclusive or shared mode.
func TryAcquireMode(path string, mode Mode) (func(), error) {
	return tryAcquireModeWithKey(path, "", mode)
}

// TryAcquireModeWithKey is the non-blocking form of AcquireModeWithKey.
func TryAcquireModeWithKey(path, localKey string, mode Mode) (func(), error) {
	if localKey == "" {
		return nil, errors.New("file lock local key is empty")
	}
	return tryAcquireModeWithKey(path, localKey, mode)
}

func tryAcquireModeWithKey(path, localKey string, mode Mode) (func(), error) {
	lockPath, err := canonicalLockPath(path)
	if err != nil {
		return nil, err
	}
	key := localKey
	if key == "" {
		key = lockPath
	}
	releaseLocal, ok := tryAcquireLocal(key, mode)
	if !ok {
		return nil, ErrHeld
	}

	releaseFile, err := tryLockFileMode(lockPath, mode)
	if err != nil {
		releaseLocal()
		if errors.Is(err, ErrHeld) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("try acquire file lock: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseFile()
			releaseLocal()
		})
	}, nil
}

func lookupLocal(key string) *localLock {
	local := localRegistry.locks[key]
	if local == nil {
		local = &localLock{}
		local.cond = sync.NewCond(&local.mu)
		localRegistry.locks[key] = local
	}
	local.refs++
	return local
}

func acquireLocal(ctx context.Context, key string, mode Mode) (func(), error) {
	localRegistry.Lock()
	local := lookupLocal(key)
	localRegistry.Unlock()

	stop := context.AfterFunc(ctx, func() {
		local.mu.Lock()
		local.cond.Broadcast()
		local.mu.Unlock()
	})
	defer stop()

	local.mu.Lock()
	writer := mode == ModeExclusive
	if writer {
		local.waitingWriters++
	}
	for {
		if ctx.Err() != nil {
			if writer {
				local.waitingWriters--
				local.cond.Broadcast()
			}
			local.mu.Unlock()
			dropLocalRef(key, local)
			return nil, fmt.Errorf("acquire file lock: %w", ctx.Err())
		}
		if mode == ModeShared {
			if !local.exclusive && local.waitingWriters == 0 {
				local.readers++
				local.mu.Unlock()
				return releaseLocalFunc(key, local, mode), nil
			}
		} else if !local.exclusive && local.readers == 0 {
			local.waitingWriters--
			local.exclusive = true
			local.mu.Unlock()
			return releaseLocalFunc(key, local, mode), nil
		}
		local.cond.Wait()
	}
}

func tryAcquireLocal(key string, mode Mode) (func(), bool) {
	localRegistry.Lock()
	local := lookupLocal(key)
	localRegistry.Unlock()

	local.mu.Lock()
	if mode == ModeShared {
		if local.exclusive || local.waitingWriters > 0 {
			local.mu.Unlock()
			dropLocalRef(key, local)
			return nil, false
		}
		local.readers++
		local.mu.Unlock()
		return releaseLocalFunc(key, local, mode), true
	}
	if local.exclusive || local.readers > 0 {
		local.mu.Unlock()
		dropLocalRef(key, local)
		return nil, false
	}
	local.exclusive = true
	local.mu.Unlock()
	return releaseLocalFunc(key, local, mode), true
}

func dropLocalRef(key string, local *localLock) {
	localRegistry.Lock()
	local.refs--
	if local.refs <= 0 {
		local.refs = 0
		delete(localRegistry.locks, key)
	}
	localRegistry.Unlock()
}

func releaseLocalFunc(key string, local *localLock, mode Mode) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			local.mu.Lock()
			if mode == ModeShared {
				if local.readers > 0 {
					local.readers--
				}
			} else {
				local.exclusive = false
			}
			local.cond.Broadcast()
			local.mu.Unlock()
			dropLocalRef(key, local)
		})
	}
}

// RegistrySizeForTest returns the number of live local-lock entries (tests).
func RegistrySizeForTest() int {
	localRegistry.Lock()
	defer localRegistry.Unlock()
	return len(localRegistry.locks)
}

// HeldPathsForTest returns the canonical paths currently holding a lock. A
// package TestMain uses it to turn a leaked lease into a failure everywhere:
// Windows cannot remove a directory containing an open lock file, so a leak
// that only breaks t.TempDir cleanup there is otherwise invisible on POSIX.
func HeldPathsForTest() []string {
	localRegistry.Lock()
	defer localRegistry.Unlock()
	held := make([]string, 0, len(localRegistry.locks))
	for path := range localRegistry.locks {
		held = append(held, path)
	}
	sort.Strings(held)
	return held
}

func canonicalLockPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("file lock path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve file lock path: %w", err)
	}
	return filepath.Clean(abs), nil
}
