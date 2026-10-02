package session

import (
	"context"
	"path/filepath"
)

// Existing but obsolete indexes need the same background lifecycle as missing
// indexes. A reader must not wait on a whole-log rebuild or its mutex.
func (q *Query) historyLocatorReady(ctx context.Context, filesystem *FilesystemPersistence, sessionID, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	lock := q.projectionLock("history", sessionID)
	if !lock.TryLock() {
		return false, nil
	}
	dir := filepath.Join(filesystem.Root, sessionID)
	revision, err := revisionOfLog(dir)
	current := err == nil && historyIndexCurrent(ctx, dir, path, sessionID, revision)
	lock.Unlock()
	if err != nil {
		return false, err
	}
	if current {
		return true, nil
	}
	preparation := q.prepareHistoryLocator(filesystem, sessionID, path, ctx)
	select {
	case <-preparation.done:
		return preparation.err == nil, preparation.err
	default:
		return false, nil
	}
}
