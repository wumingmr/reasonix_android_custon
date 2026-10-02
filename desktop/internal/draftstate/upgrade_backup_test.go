package draftstate

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestUpgradeBackupIncludesCommittedWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drafts.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, `CREATE TABLE previous_input(value TEXT)`, `PRAGMA user_version=3`, `INSERT INTO previous_input VALUES('unsent attachment and text')`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := backupBeforeUpgrade(db, path, 3); err != nil {
		t.Fatal(err)
	}
	backup, err := sql.Open("sqlite", path+".pre-v4.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var text string
	var version int
	if err := backup.QueryRow(`SELECT value FROM previous_input`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if text != "unsent attachment and text" || version != 3 {
		t.Fatalf("backup lost source: %q v%d", text, version)
	}
	if _, err := db.Exec(`UPDATE previous_input SET value='newer'`); err != nil {
		t.Fatal(err)
	}
	if err := backupBeforeUpgrade(db, path, 3); err != nil {
		t.Fatal(err)
	}
	if err := backup.QueryRow(`SELECT value FROM previous_input`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "unsent attachment and text" {
		t.Fatal("retry replaced original backup")
	}
}
