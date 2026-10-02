package projectiondb

import (
	"context"
	"database/sql"
	"fmt"
)

// Close the staging writer before publication. Cancellation retains resumable
// progress; failures discard it without changing the currently readable file.
func populateRebuildReplacement(ctx context.Context, opts OpenOptions, handle *Handle, populate func(context.Context, *sql.DB) error, cleanupTemporary func()) error {
	if populate != nil {
		if err := populate(ctx, handle.DB); err != nil {
			_ = handle.DB.Close()
			if opts.ResumeKey == "" || ctx.Err() == nil {
				cleanupTemporary()
			}
			return fmt.Errorf("populate projection replacement: %w", err)
		}
	}
	check := `PRAGMA integrity_check`
	if opts.QuickCheck {
		check = `PRAGMA quick_check(1)`
	}
	var integrity string
	if err := handle.DB.QueryRowContext(ctx, check).Scan(&integrity); err != nil || integrity != "ok" {
		_ = handle.DB.Close()
		if opts.ResumeKey == "" || ctx.Err() == nil {
			cleanupTemporary()
		}
		if err != nil {
			return fmt.Errorf("validate projection replacement: %w", err)
		}
		return fmt.Errorf("validate projection replacement: %s", integrity)
	}
	_, _ = handle.DB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := handle.DB.Close(); err != nil {
		cleanupTemporary()
		return err
	}
	if err := ctx.Err(); err != nil {
		if opts.ResumeKey == "" {
			cleanupTemporary()
		}
		return err
	}

	return nil
}
