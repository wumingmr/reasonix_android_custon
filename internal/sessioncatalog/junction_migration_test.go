package sessioncatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/projectiondb"
)

func TestNativeIdentityMigrationRebuildsOnlyProjectionOnce(t *testing.T) {
	// Version 13 already includes the identity-invalidating migration, so the
	// stale-row assertion covers only the last pre-invalidation schema. The
	// migration boundary itself is covered by TestSchemaV13RebuildsFilesystemIdentityProjection.
	for _, version := range []int{12} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			transcript := filepath.Join(root, "keep.jsonl")
			const content = "authoritative transcript must survive projection migration\n"
			if err := os.WriteFile(transcript, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "catalog.sqlite")
			old, err := projectiondb.Open(ctx, projectiondb.OpenOptions{Path: path, Migrations: sessionMigrations()[:version], Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			_, err = old.DB.ExecContext(ctx, `INSERT INTO catalog_directories(path,path_key,scope) VALUES('/alias','old-alias-key','global')`)
			if closeErr := old.DB.Close(); err != nil || closeErr != nil {
				t.Fatalf("seed legacy projection: %v / %v", err, closeErr)
			}
			catalog, err := Open(ctx, Options{Path: path, DisableRepair: true})
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := catalog.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_directories`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale identity retained: %d / %v", count, err)
			}
			_, err = catalog.db.ExecContext(ctx, `INSERT INTO catalog_directories(path,path_key,scope) VALUES('/physical','new-physical-key','global')`)
			if closeErr := catalog.Close(ctx); err != nil || closeErr != nil {
				t.Fatalf("seed rebuilt projection: %v / %v", err, closeErr)
			}
			catalog, err = Open(ctx, Options{Path: path, DisableRepair: true})
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close(ctx)
			if err := catalog.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_directories WHERE path_key='new-physical-key'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("restart discarded new projection: %d / %v", count, err)
			}
			oldReader, err := projectiondb.Open(ctx, projectiondb.OpenOptions{Path: path, MemoryName: "old-reader", Migrations: sessionMigrations()[:version], Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			defer oldReader.DB.Close()
			if oldReader.Status.Mode != projectiondb.ModeMemory || oldReader.Status.QuarantinedPath != "" {
				t.Fatalf("old reader reused or quarantined new projection: %+v", oldReader.Status)
			}
			if inspection := projectiondb.Inspect(ctx, path); inspection.Schema != len(sessionMigrations()) {
				t.Fatalf("old reader changed new schema: %+v", inspection)
			}
			if raw, err := os.ReadFile(transcript); err != nil || string(raw) != content {
				t.Fatalf("authoritative data changed: %v", err)
			}
		})
	}
}
