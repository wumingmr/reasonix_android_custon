package draftstate

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// VACUUM INTO reads a consistent SQLite snapshot including committed WAL
// frames. Copying only the main database would lose acknowledged input.
func backupBeforeUpgrade(db *sql.DB, path string, version int) error {
	backup := fmt.Sprintf("%s.pre-v%d.sqlite", path, SchemaVersion)
	if _, err := os.Stat(backup); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".draft-upgrade-*.sqlite")
	if err != nil {
		return err
	}
	temporary := file.Name()
	if err := file.Close(); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if _, err := db.Exec(`VACUUM INTO ?`, temporary); err != nil {
		return fmt.Errorf("backup draft schema %d: %w", version, err)
	}
	if err := os.Chmod(temporary, 0600); err != nil {
		return err
	}
	// Link publishes without replacing a snapshot another upgrader produced.
	if err := os.Link(temporary, backup); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}
