package projectiondb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRebuildResumesCanceledGenerationWithoutPublishingPrefix(t *testing.T) {
	for _, mode := range []string{"same-source", "replaced-source", "missing-fence", "failed-build"} {
		t.Run(mode, func(t *testing.T) {
			opts := OpenOptions{Path: filepath.Join(t.TempDir(), "cache.sqlite"), Migrations: testMigrations(), RequireDisk: true, ResumeKey: "first"}
			old, err := Open(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := old.DB.Exec(`INSERT INTO values_table VALUES('old-published')`); err != nil {
				t.Fatal(err)
			}
			if err := old.DB.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			err = Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
				if _, err := db.ExecContext(ctx, `INSERT INTO values_table VALUES('durable-prefix')`); err != nil {
					return err
				}
				cancel()
				return ctx.Err()
			})
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation missing: %v", err)
			}
			old, err = Open(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			var published string
			err = old.DB.QueryRow(`SELECT value FROM values_table`).Scan(&published)
			_ = old.DB.Close()
			if err != nil || published != "old-published" {
				t.Fatalf("partial generation became readable: %q %v", published, err)
			}
			if mode == "replaced-source" {
				opts.ResumeKey = "second"
			}
			if mode == "missing-fence" {
				pending := opts
				pending.Path += ".rebuild-pending"
				handle, err := Open(t.Context(), pending)
				if err != nil {
					t.Fatal(err)
				}
				_, err = handle.DB.Exec(`DELETE FROM projection_rebuild_resume`)
				_ = handle.DB.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "failed-build" {
				failure := errors.New("invalid source")
				if err := Rebuild(t.Context(), opts, func(context.Context, *sql.DB) error { return failure }); !errors.Is(err, failure) {
					t.Fatalf("source failure missing: %v", err)
				}
				if _, err := os.Stat(opts.Path + ".rebuild-pending"); !os.IsNotExist(err) {
					t.Fatalf("failed generation retained: %v", err)
				}
			}
			err = Rebuild(t.Context(), opts, func(ctx context.Context, db *sql.DB) error {
				var count int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM values_table`).Scan(&count); err != nil {
					return err
				}
				want := 1
				if mode != "same-source" {
					want = 0
				}
				if count != want {
					t.Fatalf("resume key did not fence progress: %d want %d", count, want)
				}
				_, err := db.ExecContext(ctx, `INSERT INTO values_table VALUES('completed')`)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(opts.Path + ".rebuild-pending"); !os.IsNotExist(err) {
				t.Fatalf("published generation left a staging database: %v", err)
			}
		})
	}
}
