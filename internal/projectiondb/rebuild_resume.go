package projectiondb

import (
	"context"
	"database/sql"
	"errors"
)

// Called only while the existing cross-process rebuild lock is held. The
// stable staging name is never opened by ordinary readers or old Rebuild calls.
func matchRebuildResumeKey(ctx context.Context, db *sql.DB, key string) (bool, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS projection_rebuild_resume(
 id INTEGER PRIMARY KEY CHECK(id=1), source_key TEXT NOT NULL)`); err != nil {
		return false, err
	}
	var stored string
	err := db.QueryRowContext(ctx, `SELECT source_key FROM projection_rebuild_resume WHERE id=1`).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.ExecContext(ctx, `INSERT INTO projection_rebuild_resume VALUES(1,?)`, key)
		// A missing fence cannot certify existing staging rows. Rebuild will
		// discard them before installing the key in a fresh database.
		return false, err
	}
	return stored == key, err
}

func resumeRebuildReplacement(ctx context.Context, handle *Handle, replacement OpenOptions, cleanupTemporary func()) (*Handle, error) {
	matching, resumeErr := matchRebuildResumeKey(ctx, handle.DB, replacement.ResumeKey)
	if resumeErr != nil {
		_ = handle.DB.Close()
		if ctx.Err() == nil {
			cleanupTemporary()
		}
		return nil, resumeErr
	}
	if !matching {
		_ = handle.DB.Close()
		cleanupTemporary()
		var err error
		handle, err = Open(ctx, replacement)
		if err != nil {
			return nil, err
		}
		if _, err := matchRebuildResumeKey(ctx, handle.DB, replacement.ResumeKey); err != nil {
			_ = handle.DB.Close()
			cleanupTemporary()
			return nil, err
		}
	}
	return handle, nil
}
