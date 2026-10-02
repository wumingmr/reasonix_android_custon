package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	filelock "reasonix/internal/identitylock"
	"reasonix/internal/topicstate"
)

// withExclusiveScope keeps the authoritative topic database and its legacy
// mirror under one cross-process lock. The callback must use the supplied
// store and must not re-enter this manager for the same workspace.
func (m *topicStateManager) withExclusiveScope(workspaceRoot string, mutate func(context.Context, *topicstate.Store) error) error {
	scope := m.scope(workspaceRoot)
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.root != "" && !existingDirectory(scope.root) {
		return fmt.Errorf("workspace root %q no longer exists", scope.root)
	}
	if scope.path == "" {
		return errors.New("topic state directory is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(scope.path), 0o700); err != nil {
		return err
	}
	lockCtx, cancelLock := context.WithTimeout(context.Background(), topicStateLockTimeout)
	release, err := filelock.Acquire(lockCtx, scope.path+".compat.lock")
	cancelLock()
	if err != nil {
		return err
	}
	defer release()
	if err := m.ensureOpenAndReconcileLocked(scope); err != nil {
		return err
	}
	ctx, cancelOperation := m.operationContext()
	defer cancelOperation()
	if mutate != nil {
		if err := mutate(ctx, scope.store); err != nil {
			return err
		}
	}
	if err := m.mirrorIfPendingLocked(ctx, scope); err != nil {
		slog.Warn("desktop: topic legacy mirror pending after maintenance", "scope", topicScopeKind(scope.root), "error_type", topicStateErrorType(err))
	}
	return nil
}
