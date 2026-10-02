package sessioncatalog

import (
	"context"
)

func (c *Catalog) persistReconcileTarget(target DirectoryTarget) error {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	_, err := c.db.ExecContext(c.workerCtx, `INSERT INTO catalog_pending_roots(path_key,path,scope,workspace_root,sequence)
		VALUES(?,?,?,?,?) ON CONFLICT(path_key) DO UPDATE SET path=excluded.path,scope=excluded.scope,
		workspace_root=excluded.workspace_root,sequence=excluded.sequence
		WHERE excluded.sequence>catalog_pending_roots.sequence`,
		queuePathKey(target.Path), target.Path, target.Scope, target.WorkspaceRoot, target.mutationSeq)
	return err
}

func (c *Catalog) settleReconcileTarget(target DirectoryTarget) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	_, _ = c.db.ExecContext(c.workerCtx, `DELETE FROM catalog_pending_roots WHERE path_key=? AND sequence<=?`, queuePathKey(target.Path), target.mutationSeq)
}

// Startup reads only dirty root identities. No directory is enumerated and no
// transcript is opened here. Failed and interrupted scans keep their journal.
func (c *Catalog) loadReconcileJournal(ctx context.Context) error {
	rows, err := c.db.QueryContext(ctx, `SELECT path,scope,workspace_root,sequence FROM catalog_pending_roots`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var target DirectoryTarget
		if err := rows.Scan(&target.Path, &target.Scope, &target.WorkspaceRoot, &target.mutationSeq); err != nil {
			return err
		}
		if target.mutationSeq > c.mutationSeq.Load() {
			c.mutationSeq.Store(target.mutationSeq)
		}
		key := queuePathKey(target.Path)
		c.reconcileQueued.Store(key, target)
		c.reconcileDirty[key] = target
	}
	return rows.Err()
}
