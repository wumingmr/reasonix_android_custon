package worktree

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// metadata.json keeps the shape older builds decode strictly, so a worktree
// this build creates stays mergeable after a downgrade.
func TestMergeMetadataStaysReadableByStrictOlderDecoder(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	created, err := Create(context.Background(), opened(t, repo), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(metadataPath(created.WorktreeRoot))
	if err != nil {
		t.Fatal(err)
	}
	type olderMetadata struct {
		Version        int    `json:"version"`
		SourceRoot     string `json:"sourceRoot"`
		TargetBranch   string `json:"targetBranch"`
		CreatedHead    string `json:"createdHead"`
		WorktreeRoot   string `json:"worktreeRoot"`
		WorktreeBranch string `json:"worktreeBranch"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var older olderMetadata
	if err := decoder.Decode(&older); err != nil {
		t.Fatalf("older decoder: %v", err)
	}
	if _, err := os.Stat(identityPath(metadataPath(created.WorktreeRoot))); err != nil {
		t.Fatalf("pinned identity was not recorded beside the metadata: %v", err)
	}
}

// A worktree created before identities were recorded resolves them from the
// recorded roots and still merges back and finalizes.
func TestMergeBackWithoutRecordedIdentity(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	managed := t.TempDir()
	created, err := Create(context.Background(), opened(t, repo), managed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(identityPath(metadataPath(created.WorktreeRoot))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created.WorktreeRoot, "feature.go"), []byte("package feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspection := inspectMergeTest(t, created.WorkspaceRoot, managed)
	request := requestFromInspection(inspection)
	request.AutoCommitDirty = true
	result, err := MergeBack(context.Background(), managed, request)
	if err != nil || !result.Merged {
		t.Fatalf("MergeBack: %v (%+v)", err, result)
	}
	if _, err := FinalizeMerge(context.Background(), managed, cleanupFromMerge(result)); err != nil {
		t.Fatalf("FinalizeMerge: %v", err)
	}
}

// A recorded identity naming another checkout's git dir is refused, not used.
func TestInspectMergeRefusesForgedRecordedIdentity(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	other := initRepo(t)
	managed := t.TempDir()
	created, err := Create(context.Background(), opened(t, repo), managed)
	if err != nil {
		t.Fatal(err)
	}
	forged := created.WorktreeRepo
	forged.GitDir = opened(t, other).GitDir
	body, err := json.Marshal(mergeIdentity{SourceRepo: created.SourceRepo, WorktreeRepo: forged})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath(metadataPath(created.WorktreeRoot)), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectMerge(context.Background(), created.WorkspaceRoot, managed); err == nil {
		t.Fatal("InspectMerge accepted a recorded identity naming another checkout")
	}
}
