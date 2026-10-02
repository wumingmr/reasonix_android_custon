package sessioncatalog

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOrdinaryViewsHideConfirmedDamageButRetainPendingRepair(t *testing.T) {
	catalog, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	dir := t.TempDir()
	for _, health := range []Health{HealthOK, HealthDegraded, HealthCorrupt, HealthMissing} {
		record := SessionRecord{Path: filepath.Join(dir, string(health)+".jsonl"), Directory: dir, Scope: "global", TopicID: string(health), Health: health, OrdinaryVisible: true}
		if err := catalog.UpsertSession(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		want := health == HealthOK || health == HealthDegraded
		if OrdinaryTreeSession(record, false, false, nil) != want {
			t.Fatalf("materialized visibility for %s", health)
		}
	}
	rows, err := catalog.ListOrdinarySessions(t.Context(), OrdinaryPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(rows) != 2 {
		t.Fatalf("paged visibility: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.Health == HealthCorrupt || row.Health == HealthMissing {
			t.Fatal("damaged row shown")
		}
	}
}
