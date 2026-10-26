package sessioncatalog

import (
	"context"
	"fmt"
)

// ProveRecoveryLineage reads session content for the directories that hold
// recovery copies and records which copies a parent already covers. Metadata
// discovery never reads content, so on its own it lists every copy as an
// ordinary conversation; this is the separate, caller-scheduled step that earns
// the right to hide one. A directory whose content cannot be read keeps its
// copies visible, and directories without recovery copies are not touched.
func (c *Catalog) ProveRecoveryLineage(ctx context.Context) error {
	if c == nil || c.db == nil || !c.opts.MetadataOnly {
		return nil
	}
	rows, err := c.readDB(ctx).QueryContext(ctx, `SELECT DISTINCT directory,scope,workspace_root
		FROM catalog_sessions WHERE missing_since=0 AND recovered=1`)
	if err != nil {
		return fmt.Errorf("list directories with recovery copies: %w", err)
	}
	var targets []DirectoryTarget
	for rows.Next() {
		var target DirectoryTarget
		if err := rows.Scan(&target.Path, &target.Scope, &target.WorkspaceRoot); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, target)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.proveDirectoryLineage(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) proveDirectoryLineage(ctx context.Context, target DirectoryTarget) error {
	if c.opts.Maintenance != nil {
		ctx = c.opts.Maintenance.Context(ctx)
		release, err := c.opts.Maintenance.BackgroundSlice(ctx, false)
		if err != nil {
			return err
		}
		defer release(0)
	}
	return c.reconcileDirectoryFull(ctx, target, c.mutationSeq.Add(1))
}
