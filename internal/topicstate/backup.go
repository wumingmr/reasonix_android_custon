package topicstate

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"reasonix/internal/sqliteuri"
)

// BackupExisting reads an existing database without running schema migrations.
// VACUUM INTO captures a consistent SQLite snapshot including committed WAL
// pages and unknown tables/columns. The destination must not already exist.
func BackupExisting(ctx context.Context, path, destination string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("topic state is not a regular file")
	}
	if _, err := os.Lstat(destination); err == nil {
		return &os.PathError{Op: "backup", Path: destination, Err: os.ErrExist}
	} else if !os.IsNotExist(err) {
		return err
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	dsn, err := sqliteuri.Disk(path, q)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	// Build the snapshot privately and publish it without replacement. VACUUM
	// itself accepts existing empty files, and a preflight existence check alone
	// cannot protect a destination created while the snapshot is being built.
	parent, err := filepath.Abs(filepath.Dir(destination))
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".topic-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, "snapshot.sqlite")
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snapshot); err != nil {
		return err
	}
	if err := os.Chmod(snapshot, 0o600); err != nil {
		return err
	}
	return os.Link(snapshot, destination)
}
