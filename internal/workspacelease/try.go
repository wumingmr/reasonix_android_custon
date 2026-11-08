package workspacelease

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"reasonix/internal/filelock"
)

// ConflictError reports a refused acquisition, not a queued write. Target is
// the requested lock domain; advisory locks cannot identify the other session.
type ConflictError struct {
	Scope  string
	Target string
	Reason string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s write conflict at %q: %s", e.Scope, e.Target, e.Reason)
}

func (e *ConflictError) Unwrap() error { return filelock.ErrHeld }

type tryWriteKey struct{}

// TryHoldWrite preserves the workspace/legacy lock protocol, but never queues
// behind a writer or emits a wait notice. The caller must handle the conflict.
func (o *Owner) TryHoldWrite(ctx context.Context) (func(), error) {
	return o.HoldWrite(tryWriteContext(ctx))
}

// TryHoldWriteForPaths is the non-waiting form of HoldWriteForPaths. It retains
// canonical identity checks, atomic multi-path rollback and Owner re-entrancy.
func (o *Owner) TryHoldWriteForPaths(ctx context.Context, paths []string) (func(), error) {
	return o.HoldWriteForPaths(tryWriteContext(ctx), paths)
}

func tryWriteContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, tryWriteKey{}, true)
}

func isTryWrite(ctx context.Context) bool {
	return ctx.Value(tryWriteKey{}) == true
}

func (o *Owner) localConflictLocked(reason string) error {
	return &ConflictError{Scope: "session", Target: o.canonical, Reason: reason}
}

func (o *Owner) systemConflict(path string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	scope, target := "workspace", o.canonical
	reason := "another writer holds this workspace/hierarchy domain, or its acquisition queue is busy"
	if strings.HasPrefix(filepath.Base(path), "path-") {
		scope = "file"
		var targets []string
		for _, key := range o.lease.targetKeys {
			if o.pathLockPath(key) == path {
				targets = append(targets, key)
			}
		}
		target = strings.Join(targets, ", ")
		reason = "another writer holds the requested file lock stripe (rare hash collisions can also protect a different file)"
	}
	return &ConflictError{Scope: scope, Target: target, Reason: reason}
}
