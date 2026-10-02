package projectiondb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestAdvisoryOpenRetainsFullAuditAndDefaultQuarantine(t *testing.T) {
	opts := OpenOptions{Path: filepath.Join(t.TempDir(), "advisory.sqlite"), RequireDisk: true,
		Migrations: []Migration{{Version: 1, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE checked(value INTEGER CHECK(value>=0))`)
			return err
		}}}}
	seed, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.DB.Exec(`PRAGMA ignore_check_constraints=ON; INSERT INTO checked VALUES(-1)`); err != nil {
		t.Fatal(err)
	}
	if err := seed.DB.Close(); err != nil {
		t.Fatal(err)
	}
	advisory, err := OpenAdvisory(t.Context(), opts)
	if err != nil {
		t.Fatalf("advisory open blocked on full audit: %v", err)
	}
	if advisory.Status.QuarantinedPath != "" {
		t.Fatal("advisory open quarantined before audit")
	}
	if err := CheckIntegrity(t.Context(), advisory.DB); !IsCorruptionError(err) {
		t.Fatalf("full deferred audit missed constraint corruption: %v", err)
	}
	if err := advisory.DB.Close(); err != nil {
		t.Fatal(err)
	}
	validated, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer validated.DB.Close()
	if validated.Status.QuarantinedPath == "" {
		t.Fatal("normal open no longer quarantines")
	}
	if err := CheckIntegrity(t.Context(), validated.DB); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedJSONQueryIsNotDatabaseCorruption(t *testing.T) {
	h, err := Open(t.Context(), OpenOptions{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	defer h.DB.Close()
	var value any
	err = h.DB.QueryRow(`SELECT json_extract('not json','$')`).Scan(&value)
	if err == nil || IsCorruptionError(err) {
		t.Fatalf("query error misclassified: %v", err)
	}
}
