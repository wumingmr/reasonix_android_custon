package projectiondb

import (
	"context"
	"database/sql"
	"fmt"
)

// CheckIntegrity performs the full SQLite audit, including index consistency.
// It is separate from advisory metadata opening, never replaced by quick_check.
func CheckIntegrity(ctx context.Context, db *sql.DB) error {
	return checkDatabaseIntegrity(ctx, db, false)
}

func checkDatabaseIntegrity(ctx context.Context, db *sql.DB, quick bool) error {
	query := `PRAGMA integrity_check`
	if quick {
		query = `PRAGMA quick_check(1)`
	}
	var result string
	if err := db.QueryRowContext(ctx, query).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("projection integrity check: %s", result)
	}
	return nil
}

// IsCorruptionError distinguishes a damaged cache from contention or cancelled
// I/O. Only proven corruption may revoke a generation and quarantine its file.
func IsCorruptionError(err error) bool { return isCorruptionError(err) }
