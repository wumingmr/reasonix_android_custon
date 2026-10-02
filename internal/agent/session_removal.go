package agent

import (
	"errors"
	"fmt"
	"os"

	"reasonix/internal/pathidentity"
	"reasonix/internal/store"
)

// SessionRemovalGuard holds a session's save lock and lease lock for the
// duration of a destructive operation (trash, purge, permanent delete). While
// held, no other runtime can acquire the session lease and no saver can write
// the transcript, so artifacts can be moved or deleted without racing a live
// owner; the lock files themselves are then deleted atomically with the
// release (unlink-under-flock on Unix, delete-disposition on Windows), so a
// later acquirer can never lock an inode that survived the deletion.
//
// This closes the probe-then-delete window: a one-shot busy check followed by
// a plain RemoveAll lets another process acquire the lease between the two
// steps and then loses its lock file, breaking cross-process mutual exclusion.
type SessionRemovalGuard struct {
	path            string
	key             string
	saveLock        *sessionLockFile
	leaseLock       *sessionLockFile
	legacyLeaseLock *sessionLockFile
	legacyPath      string
	restoreOwner    uint64
}

func tryTakeSessionLeaseLock(path string) (*sessionLockFile, error) {
	lock, err := tryTakeSessionLeaseLockFile(store.SessionLeaseLock(path))
	if errors.Is(err, ErrSessionFileLockHeld) {
		return nil, ErrSessionLeaseHeld
	}
	return lock, err
}

// TryAcquireSessionRemovalGuard takes both locks without blocking. A live
// holder of either — including a lease held elsewhere in this process —
// surfaces as ErrSessionLeaseHeld so callers report the session as busy
// instead of deleting files out from under a running owner.
func TryAcquireSessionRemovalGuard(path string) (*SessionRemovalGuard, error) {
	identity, err := resolveSessionPathIdentity(path)
	if err != nil {
		return nil, err
	}
	path, key := identity.PhysicalPath, identity.Key
	legacyPath := pathidentity.Canonical(identity.AccessPath)
	if sessionLeaseHeldLocally(key) {
		info, _ := LoadSessionLeaseInfo(path)
		return nil, &SessionLeaseError{Path: key, Info: info}
	}
	leaseLock, legacyLeaseLock, err := tryTakeCompatibleSessionLeaseLocks(path, legacyPath)
	if err != nil {
		if errors.Is(err, ErrSessionLeaseHeld) {
			info, _ := LoadSessionLeaseInfo(path)
			return nil, &SessionLeaseError{Path: key, Info: info}
		}
		return nil, err
	}
	saveLock, err := tryTakeSessionLockFile(store.SessionLockFile(path))
	if err != nil {
		unlockSessionLeaseLocks(leaseLock, legacyLeaseLock)
		if errors.Is(err, ErrSessionFileLockHeld) {
			// A save is in flight; deleting mid-write would race it.
			return nil, &SessionLeaseError{Path: key}
		}
		return nil, err
	}
	return &SessionRemovalGuard{
		path: path, key: key, legacyPath: legacyPath,
		saveLock: saveLock, leaseLock: leaseLock, legacyLeaseLock: legacyLeaseLock,
	}, nil
}

// TryConvertToRemovalGuard transfers a live lease into destructive ownership
// without ever releasing its lease lock. The save lock is acquired first, so a
// failed conversion leaves the original lease fully active and reusable.
func (l *SessionLease) TryConvertToRemovalGuard() (*SessionRemovalGuard, error) {
	if l == nil {
		return nil, fmt.Errorf("nil session lease")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.leaseLock == nil {
		return nil, &SessionLeaseError{Path: l.path}
	}
	saveLock, err := tryTakeSessionLockFile(store.SessionLockFile(l.accessPath))
	if err != nil {
		if errors.Is(err, ErrSessionFileLockHeld) {
			return nil, &SessionLeaseError{Path: l.path}
		}
		return nil, err
	}
	// Keep the OS lock and original process-local reservation while revoking
	// active runtime ownership. Both block competing acquisition and make
	// rollback immune to an in-process lease race.
	sessionLeaseActiveOwners.CompareAndDelete(l.path, l.ownerID)
	leaseLock := l.leaseLock
	legacyLeaseLock := l.legacyLeaseLock
	l.leaseLock = nil
	l.legacyLeaseLock = nil
	l.released = true
	return &SessionRemovalGuard{
		path:            l.accessPath,
		key:             l.path,
		saveLock:        saveLock,
		leaseLock:       leaseLock,
		legacyLeaseLock: legacyLeaseLock,
		legacyPath:      l.legacyAccessPath,
		restoreOwner:    l.ownerID,
	}, nil
}

// RestoreSessionLease aborts a converted removal guard before the destructive
// commit point. The original lease metadata remains untouched and both locks
// stay held until its owner generation is republished, so rollback cannot fail
// on a second disk write and has no writer gap.
func (g *SessionRemovalGuard) RestoreSessionLease() (*SessionLease, error) {
	if g == nil || g.restoreOwner == 0 || g.leaseLock == nil || g.saveLock == nil {
		return nil, fmt.Errorf("removal guard cannot restore a session lease")
	}
	ownerID := g.restoreOwner
	current, ok := sessionLeaseOwners.Load(g.key)
	if !ok || current != ownerID {
		return nil, &SessionLeaseError{Path: g.path}
	}
	sessionLeaseActiveOwners.Delete(g.key)
	sessionLeaseActiveOwners.Store(g.key, ownerID)
	lease := &SessionLease{
		path: g.key, accessPath: g.path, legacyAccessPath: g.legacyPath,
		ownerID: ownerID, leaseLock: g.leaseLock, legacyLeaseLock: g.legacyLeaseLock,
	}
	g.leaseLock = nil
	g.legacyLeaseLock = nil
	g.saveLock.Unlock()
	g.saveLock = nil
	g.restoreOwner = 0
	return lease, nil
}

// Release ends the guard without deleting the lock files — the abort path
// when the destructive operation did not happen. Safe to call after
// RemoveSidecarsAndRelease (it becomes a no-op).
func (g *SessionRemovalGuard) Release() {
	if g == nil {
		return
	}
	if g.restoreOwner != 0 {
		// A converted lease no longer has an active owner. Do not leave its
		// identity sidecar naming a writer after an abort that chose not to
		// restore the runtime lease.
		sessionLeaseActiveOwners.CompareAndDelete(g.key, g.restoreOwner)
		sessionLeaseOwners.CompareAndDelete(g.key, g.restoreOwner)
		_ = os.Remove(store.SessionLeaseInfo(g.path))
		g.restoreOwner = 0
	}
	if g.saveLock != nil {
		g.saveLock.Unlock()
		g.saveLock = nil
	}
	if g.leaseLock != nil {
		g.leaseLock.Unlock()
		g.leaseLock = nil
	}
	if g.legacyLeaseLock != nil {
		g.legacyLeaseLock.Unlock()
		g.legacyLeaseLock = nil
	}
}

// RemoveSidecarsAndRelease deletes the lease info and both lock files
// atomically with the release, then ends the guard. The lease info goes first,
// while the lease lock is still held, so no probe can adopt it mid-removal.
func (g *SessionRemovalGuard) RemoveSidecarsAndRelease() error {
	if g == nil {
		return nil
	}
	var errs []error
	if g.restoreOwner != 0 {
		sessionLeaseActiveOwners.CompareAndDelete(g.key, g.restoreOwner)
		sessionLeaseOwners.CompareAndDelete(g.key, g.restoreOwner)
	}
	if err := os.Remove(store.SessionLeaseInfo(g.path)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	if g.saveLock != nil {
		if err := g.saveLock.RemoveAndUnlock(); err != nil {
			errs = append(errs, err)
		}
		g.saveLock = nil
	}
	if g.leaseLock != nil {
		if err := g.leaseLock.RemoveAndUnlock(); err != nil {
			errs = append(errs, err)
		}
		g.leaseLock = nil
	}
	if g.legacyLeaseLock != nil {
		if err := g.legacyLeaseLock.RemoveAndUnlock(); err != nil {
			errs = append(errs, err)
		}
		g.legacyLeaseLock = nil
	}
	g.restoreOwner = 0
	return errors.Join(errs...)
}
