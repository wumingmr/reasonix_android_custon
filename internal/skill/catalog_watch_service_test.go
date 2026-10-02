package skill

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"reasonix/internal/skill/skillwatch"
)

func catalogNames(snapshot CatalogSnapshot) []string {
	names := make([]string, 0, len(snapshot.Candidates))
	for _, candidate := range snapshot.Candidates {
		names = append(names, candidate.Name)
	}
	sort.Strings(names)
	return names
}

func sameCatalogNames(a, b CatalogSnapshot) bool {
	return slices.Equal(catalogNames(a), catalogNames(b))
}

func waitForCatalog(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Two stores over the same roots must share the service's physical watches,
// and a discovery-relevant change must invalidate both catalogs through the
// single coalesced notification.
func TestHostWatchServiceSharedAcrossStores(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".reasonix", "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "alpha", "SKILL.md"), []byte("---\nname: alpha\ndescription: first\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := skillwatch.NewService(skillwatch.Options{Stderr: io.Discard})
	defer svc.Close()

	storeA := New(Options{HomeDir: t.TempDir(), ReasonixHomeDir: t.TempDir(), ProjectRoot: root, Stderr: io.Discard, Watch: true, WatchService: svc})
	storeB := New(Options{HomeDir: t.TempDir(), ReasonixHomeDir: t.TempDir(), ProjectRoot: root, Stderr: io.Discard, Watch: true, WatchService: svc})
	defer storeA.Close()
	defer storeB.Close()

	firstA, err := storeA.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	firstB, err := storeB.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Version is each store's own rebuild counter, not a shared clock. Sharing
	// guarantees identical discovered content, so compare that.
	if !sameCatalogNames(firstA, firstB) {
		t.Fatalf("shared roots produced different catalogs: %v vs %v", catalogNames(firstA), catalogNames(firstB))
	}
	diag, ok := storeA.WatchDiagnostics()
	if !ok || diag.PhysicalWatches == 0 {
		t.Fatalf("service watches not armed: %+v active=%v", diag, ok)
	}
	if diag.LogicalSubscriptions < 2 {
		t.Fatalf("expected two logical subscriptions, got %+v", diag)
	}

	// Installing a new skill directory must invalidate both stores.
	beta := filepath.Join(skillsDir, "beta")
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta, "SKILL.md"), []byte("---\nname: beta\ndescription: second\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForCatalog(t, "storeA invalidation", func() bool {
		snap, err := storeA.Snapshot(t.Context())
		return err == nil && snap.Version > firstA.Version
	})
	waitForCatalog(t, "storeB invalidation", func() bool {
		snap, err := storeB.Snapshot(t.Context())
		return err == nil && snap.Version > firstB.Version
	})
	snap, err := storeA.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sk := range snap.Candidates {
		if sk.Name == "beta" {
			found = true
		}
	}
	if !found {
		t.Fatal("beta skill not discovered after invalidation")
	}

	// Closing both stores must release the shared physical watches.
	storeA.Close()
	storeB.Close()
	waitForCatalog(t, "physical watch teardown", func() bool {
		d := svc.Diagnostics()
		return d.PhysicalWatches == 0 && d.LogicalSubscriptions == 0
	})
}

// Changes confined to discovery-skipped bodies (scripts/, assets/, ...)
// must not invalidate the catalog through the watch service.
func TestHostWatchServiceIgnoresSkippedBodies(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".reasonix", "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "alpha", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "alpha", "SKILL.md"), []byte("---\nname: alpha\ndescription: first\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := skillwatch.NewService(skillwatch.Options{Stderr: io.Discard})
	defer svc.Close()

	store := New(Options{HomeDir: t.TempDir(), ReasonixHomeDir: t.TempDir(), ProjectRoot: root, Stderr: io.Discard, Watch: true, WatchService: svc})
	defer store.Close()
	snap, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "alpha", "scripts", "tool.sh"), []byte("echo hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	after, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != snap.Version {
		t.Fatalf("scripts/ body change bumped catalog version %d -> %d", snap.Version, after.Version)
	}
}
