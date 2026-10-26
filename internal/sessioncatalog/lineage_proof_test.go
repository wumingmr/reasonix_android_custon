package sessioncatalog

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func saveRecoveryCopies(t *testing.T, dir string, copies int) string {
	t.Helper()
	root := filepath.Join(dir, "root.jsonl")
	saveLineageSession(t, root, "q", "a", "next", "answer")
	for range copies {
		branch := agent.NewSession("sys")
		branch.Add(provider.Message{Role: provider.RoleUser, Content: "q"})
		branch.Add(provider.Message{Role: provider.RoleAssistant, Content: "a"})
		if _, err := branch.SaveConflictRecoveryBranch(agent.RecoveryBranchOptions{OriginalPath: root}); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func openMetadataCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "v10.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	return catalog
}

func ordinaryPaths(t *testing.T, catalog *Catalog, dir string) []string {
	t.Helper()
	rows, err := catalog.ListOrdinarySessions(t.Context(), OrdinaryPageRequest{Scope: "global", WorkspaceRoot: dir, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(rows))
	for _, row := range rows {
		paths = append(paths, row.Path)
	}
	return paths
}

func TestProveRecoveryLineageHidesCopiesAMetadataScanListed(t *testing.T) {
	dir := t.TempDir()
	root := saveRecoveryCopies(t, dir, 3)
	catalog := openMetadataCatalog(t)
	loads := 0
	catalog.testSessionContentLoadHook = func(string) { loads++ }
	if err := catalog.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	if got := ordinaryPaths(t, catalog, dir); len(got) != 4 || loads != 0 {
		t.Fatalf("before the proof: %d rows, %d content loads; discovery alone lists every copy and reads nothing", len(got), loads)
	}
	if err := catalog.ProveRecoveryLineage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := ordinaryPaths(t, catalog, dir); len(got) != 1 || got[0] != root {
		t.Fatalf("after the proof: %v, want only %s", got, filepath.Base(root))
	}
}

func TestProveRecoveryLineageMatchesTheFullReconcile(t *testing.T) {
	dir := t.TempDir()
	saveRecoveryCopies(t, dir, 2)
	diverged := agent.NewSession("sys")
	for i, message := range []string{"q", "a", "something else", "other answer"} {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		diverged.Add(provider.Message{Role: role, Content: message})
	}
	if _, err := diverged.SaveConflictRecoveryBranch(agent.RecoveryBranchOptions{OriginalPath: filepath.Join(dir, "root.jsonl")}); err != nil {
		t.Fatal(err)
	}
	saveLineageSession(t, filepath.Join(dir, "plain.jsonl"), "x", "y")

	full, err := Open(t.Context(), Options{InMemory: true, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = full.Close(context.Background()) })
	if err := full.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	want := ordinaryPaths(t, full, dir)

	catalog := openMetadataCatalog(t)
	if err := catalog.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ProveRecoveryLineage(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := ordinaryPaths(t, catalog, dir)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("visible after the proof = %v, full reconcile = %v", got, want)
	}
	if len(want) >= 5 {
		t.Fatalf("fixture hid nothing: %v", want)
	}
}

func TestProveRecoveryLineageLeavesPlainDirectoriesAlone(t *testing.T) {
	dir := t.TempDir()
	saveLineageSession(t, filepath.Join(dir, "a.jsonl"), "q", "a")
	saveLineageSession(t, filepath.Join(dir, "b.jsonl"), "q", "a")
	catalog := openMetadataCatalog(t)
	if err := catalog.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	loads := 0
	catalog.testSessionContentLoadHook = func(string) { loads++ }
	if err := catalog.ProveRecoveryLineage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if loads != 0 || len(ordinaryPaths(t, catalog, dir)) != 2 {
		t.Fatalf("a directory without recovery copies was read (%d loads) or changed", loads)
	}
}

func TestProveRecoveryLineageSurvivesARescan(t *testing.T) {
	dir := t.TempDir()
	root := saveRecoveryCopies(t, dir, 2)
	catalog := openMetadataCatalog(t)
	if err := catalog.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ProveRecoveryLineage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	if got := ordinaryPaths(t, catalog, dir); len(got) != 1 || got[0] != root {
		t.Fatalf("a metadata rescan undid the proof: %v", got)
	}
}
