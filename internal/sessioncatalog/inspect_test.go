package sessioncatalog

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectReturnsCompleteHealthyStatistics(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog %20 # 会话.sqlite")
	catalog, err := Open(ctx, Options{Path: path, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE catalog_state SET revision=42 WHERE id=1`,
		`INSERT INTO catalog_sessions(path,path_key,directory,directory_key,scope,topic_id,turns_state,repair_state,repair_error_kind)
		 VALUES('/sessions/a.jsonl','/sessions/a.jsonl','/sessions','/sessions','global','topic-a','unknown','blocked','parse')`,
		`INSERT INTO catalog_topics(scope,workspace_root_key,topic_id) VALUES('global','','topic-a')`,
	} {
		if _, err := catalog.db.ExecContext(ctx, statement); err != nil {
			_ = catalog.Close(ctx)
			t.Fatal(err)
		}
	}
	if err := catalog.Close(ctx); err != nil {
		t.Fatal(err)
	}

	status, err := Inspect(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateReady || status.Revision != 42 || status.Indexed != 1 || status.PhysicalSessions != 1 || status.LogicalSessions != 1 {
		t.Fatalf("unexpected inspection: %+v", status)
	}
	if status.RepairPending != 1 || status.RepairBlocked != 1 || status.RepairErrorKinds["parse"] != 1 {
		t.Fatalf("repair statistics: %+v", status)
	}
}

func TestInspectReportsQueryFailureAsDegradedWithoutPartialStatistics(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.sqlite")
	catalog, err := Open(ctx, Options{Path: path, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.db.ExecContext(ctx, `UPDATE catalog_state SET revision=42 WHERE id=1; DROP TABLE catalog_sessions`); err != nil {
		_ = catalog.Close(ctx)
		t.Fatal(err)
	}
	if err := catalog.Close(ctx); err != nil {
		t.Fatal(err)
	}

	status, err := Inspect(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateDegraded || status.Revision != 0 || status.Indexed != 0 {
		t.Fatalf("query failure published partial statistics: %+v", status)
	}
	if !strings.Contains(status.LastError, "count indexed sessions") {
		t.Fatalf("last error lacks failing stage: %q", status.LastError)
	}
}

func TestInspectReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Inspect(ctx, filepath.Join(t.TempDir(), "missing.sqlite"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect error = %v, want context cancellation", err)
	}
}

func TestInspectReadsCommittedWALWhileCatalogIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.sqlite")
	catalog, err := Open(t.Context(), Options{Path: path, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(t.Context())
	if _, err := catalog.db.ExecContext(t.Context(), `PRAGMA wal_autocheckpoint=0; UPDATE catalog_state SET revision=42 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(t.Context(), path)
	if err != nil || status.State != StateReady || status.Revision != 42 {
		t.Fatalf("live WAL inspection = %+v, %v", status, err)
	}
}
