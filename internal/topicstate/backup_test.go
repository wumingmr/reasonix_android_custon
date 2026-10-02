package topicstate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupExistingIncludesWALAndUnknownTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source %20 # 主题.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE future_data(value TEXT); INSERT INTO future_data VALUES ('preserved');`); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := BackupExisting(t.Context(), path, dest); err != nil {
		t.Fatal(err)
	}
	backup, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var value string
	if err := backup.QueryRow("SELECT value FROM future_data").Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("backup value=%q err=%v", value, err)
	}
	if err := BackupExisting(t.Context(), filepath.Join(t.TempDir(), "missing"), dest); !os.IsNotExist(err) {
		t.Fatalf("missing source: %v", err)
	}
}

func TestBackupExistingRejectsEmptyDestination(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.sqlite")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE preserved(value TEXT); INSERT INTO preserved VALUES('source')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "existing.sqlite")
	if err := os.WriteFile(destination, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := BackupExisting(t.Context(), source, destination); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination error = %v, want ErrExist", err)
	}
	if got, err := os.ReadFile(destination); err != nil || len(got) != 0 {
		t.Fatalf("existing empty file changed: %d bytes, %v", len(got), err)
	}
}

func TestBackupExistingDoesNotOverwriteDestinationOrSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE preserved(value TEXT); INSERT INTO preserved VALUES('source')`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "existing.sqlite")
	destinationBefore := []byte("do not replace")
	if err := os.WriteFile(destination, destinationBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := BackupExisting(t.Context(), path, destination); err == nil {
		t.Fatal("backup unexpectedly replaced an existing destination")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, sourceBefore) {
		t.Fatalf("source changed: err=%v", err)
	}
	if got, err := os.ReadFile(destination); err != nil || !bytes.Equal(got, destinationBefore) {
		t.Fatalf("destination changed: err=%v got=%q", err, got)
	}
}

func TestBackupFailureDoesNotPublishPartialDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "corrupt.sqlite")
	original := []byte("not a SQLite database")
	if err := os.WriteFile(source, original, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "backup.sqlite")
	if err := BackupExisting(t.Context(), source, destination); err == nil {
		t.Fatal("corrupt source must fail backup")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed backup published a destination: %v", err)
	}
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("failed backup modified source: %q, %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("backup left temporary artifacts: %+v, %v", entries, err)
	}
}

func TestBackupDoesNotReplaceDestinationCreatedDuringSnapshot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.sqlite")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE preserved(value TEXT); INSERT INTO preserved VALUES('source'); BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	destination := filepath.Join(root, "backup.sqlite")
	done := make(chan struct{})
	var backupErr error
	go func() {
		backupErr = BackupExisting(ctx, source, destination)
		close(done)
	}()
	defer func() {
		cancel()
		_, _ = db.Exec(`ROLLBACK`)
		<-done
	}()
	// The exclusive source lock prevents VACUUM from finishing. Observing its
	// private directory proves the initial destination check has already passed.
	deadline := time.Now().Add(3 * time.Second)
	for {
		paths, err := filepath.Glob(filepath.Join(root, ".topic-backup-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backup did not reach the locked snapshot stage")
		}
		time.Sleep(10 * time.Millisecond)
	}
	original := []byte("created by another process after preflight")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`COMMIT`); err != nil {
		t.Fatal(err)
	}
	<-done
	if !errors.Is(backupErr, os.ErrExist) {
		t.Fatalf("concurrent destination error=%v, want ErrExist", backupErr)
	}
	if got, err := os.ReadFile(destination); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("concurrent destination changed: %q, %v", got, err)
	}
}
