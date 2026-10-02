package workspacestate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoveryRechecksConcurrentWorkspaceOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	scanner, writer := NewStore(path), NewStore(path)
	root := t.TempDir()
	// The scanner derived an ID before another writer registered this root.
	candidate := Workspace{ID: "derived-after-upgrade", Root: root, Title: "stale", Visible: true}
	if err := writer.EnsureWorkspace(t.Context(), Workspace{ID: "persisted-owner", Root: root, Title: "User title", Visible: false}); err != nil {
		t.Fatal(err)
	}
	if err := scanner.ReconcileDiscoveredSession(t.Context(), RecoveryEntry{ID: "discovered", SessionID: "orphan"}, &candidate); err != nil {
		t.Fatal(err)
	}
	state, err := NewStore(path).Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	owner := state.Workspaces["persisted-owner"]
	if len(state.Workspaces) != 1 || len(owner.SessionIDs) != 1 || owner.SessionIDs[0] != "orphan" || owner.Title != "User title" || owner.Visible {
		t.Fatalf("discovery replaced physical ownership or presentation: %+v", state.Workspaces)
	}
}

func TestDiscoveryRejectsWorkspaceIDForAnotherDirectory(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: "collision", Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	err := store.ReconcileDiscoveredSession(t.Context(), RecoveryEntry{ID: "discovered", SessionID: "orphan"}, &Workspace{ID: "collision", Root: t.TempDir()})
	if !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("discovery attached session to a different directory: %v", err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workspaces["collision"].SessionIDs) != 0 {
		t.Fatal("failed discovery changed session ownership")
	}
}

func TestDiscoveryRechecksConcurrentOwnership(t *testing.T) {
	for _, phase := range []string{"prepared", "attached", "archived"} {
		t.Run(phase, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "state.json"))
			ctx := t.Context()
			if err := store.EnsureWorkspace(ctx, Workspace{ID: "global", Root: t.TempDir(), Visible: true}); err != nil {
				t.Fatal(err)
			}
			// A scanner has already observed an empty registry. A different
			// writer publishes/reserves the same ID before its scan finishes.
			if err := store.BeginCreate(ctx, PendingCreate{OperationID: "create", WorkspaceID: "global", SessionID: "new"}); err != nil {
				t.Fatal(err)
			}
			if phase != "prepared" {
				if err := store.AttachSession(ctx, "create", "global", "new", ""); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "archived" {
				if err := store.ArchiveSession(ctx, "new"); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := store.Load(ctx)
			entry := RecoveryEntry{ID: "canonical-new", SessionID: "new", Format: "canonical", Status: "pending", Reason: "workspace_conflict"}
			for _, workspace := range []*Workspace{nil, {ID: "wrong", Root: t.TempDir(), Visible: true}} {
				if err := store.ReconcileDiscoveredSession(ctx, entry, workspace); err != nil {
					t.Fatal(err)
				}
			}
			after, _ := store.Load(ctx)
			if after.Generation != before.Generation || len(after.RecoveryEntries) != 0 || len(after.Workspaces) != 1 {
				t.Fatalf("stale discovery changed managed session: %+v", after)
			}
		})
	}
}

func TestSessionTopicSurvivesReopenWithoutOverwritingPresentation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: "global", Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(ctx, "", "global", "session", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSessionTopic(ctx, "session", "topic", "Initial"); err != nil {
		t.Fatal(err)
	}
	title, pinned := "Edited", true
	if err := store.UpdatePresentation(ctx, []string{"session"}, &title, &pinned); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(path)
	if err := reopened.EnsureSessionTopic(ctx, "session", "stale-tab-topic", "Stale"); err != nil {
		t.Fatal(err)
	}
	state, err := reopened.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	value := state.Presentation["session"]
	if value.TopicID != "topic" || value.Title != title || !value.Pinned {
		t.Fatalf("presentation overwritten: %+v", value)
	}
}

func TestPurgeTombstoneAndRestoreAreOrderedByDurableCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, second := NewStore(path), NewStore(path)
	ctx := t.Context()
	if err := first.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := first.AttachSession(ctx, "", GlobalWorkspaceID, "victim", ""); err != nil {
		t.Fatal(err)
	}
	if err := first.ArchiveSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	state, err := first.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.BeginPurge(ctx, "victim", state.Generation); err != nil {
		t.Fatal(err)
	}
	state, err = second.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates["victim"].Lifecycle != Deleted || ClassifyPurge(state, "victim") != PurgeTombstoned {
		t.Fatalf("purge commit was not atomic: lifecycle=%+v purge=%v", state.SessionStates["victim"], ClassifyPurge(state, "victim"))
	}
	if err := second.RestoreSession(ctx, "victim"); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("restore after tombstone = %v, want mutation conflict", err)
	}
}

func TestRestoreClearsLegacyPreparedPurgeBeforeRearchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, second := NewStore(path), NewStore(path)
	ctx := t.Context()
	if err := first.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := first.AttachSession(ctx, "", GlobalWorkspaceID, "victim", ""); err != nil {
		t.Fatal(err)
	}
	if err := first.ArchiveSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	var legacy Operation
	if err := first.mutate(ctx, func(state *State) error {
		status := state.SessionStates["victim"]
		legacy = Operation{ID: "purge-victim", Kind: "purge", Phase: "prepared", Lifecycle: Deleted, SessionIDs: []string{"victim"}, ExpectedGeneration: status.Generation}
		state.PendingOperations[legacy.ID] = legacy
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := second.RestoreSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	state, err := first.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates["victim"].Lifecycle != Active || ClassifyPurge(state, "victim") != PurgeAbsent {
		t.Fatalf("restore did not supersede prepared purge: lifecycle=%+v purge=%v", state.SessionStates["victim"], ClassifyPurge(state, "victim"))
	}
	if err := first.ArchiveSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	state, _ = first.Load(ctx)
	if err := first.BeginPurge(ctx, "victim", state.Generation); err != nil {
		t.Fatalf("new explicit purge: %v", err)
	}
	if err := second.ResumePurge(ctx, "victim", legacy); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("old replay replaced new purge: %v", err)
	}
	state, _ = first.Load(ctx)
	if ClassifyPurge(state, "victim") != PurgeTombstoned {
		t.Fatalf("new purge changed by old replay: %+v", state.PendingOperations["purge-victim"])
	}
}

func TestOnlyObservedReplayCanAdvanceLegacyPreparedPurge(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(ctx, "", GlobalWorkspaceID, "victim", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	var legacy Operation
	if err := store.mutate(ctx, func(state *State) error {
		status := state.SessionStates["victim"]
		legacy = Operation{ID: "purge-victim", Kind: "purge", Phase: "prepared", Lifecycle: Deleted, SessionIDs: []string{"victim"}, ExpectedGeneration: status.Generation}
		state.PendingOperations[legacy.ID] = legacy
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginPurge(ctx, "victim", state.Generation); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("new request adopted legacy prepare: %v", err)
	}
	if err := store.ResumePurge(ctx, "victim", legacy); err != nil {
		t.Fatalf("observed replay did not advance prepare: %v", err)
	}
	state, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ClassifyPurge(state, "victim") != PurgeTombstoned || state.SessionStates["victim"].Lifecycle != Deleted {
		t.Fatalf("legacy replay was not atomically tombstoned: %+v", state)
	}
}

func TestInvalidPreparedDeletedPurgePreservesEvidence(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(ctx, "", GlobalWorkspaceID, "victim", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	var malformed Operation
	if err := store.mutate(ctx, func(state *State) error {
		status := state.SessionStates["victim"]
		status.Lifecycle = Deleted
		state.SessionStates["victim"] = status
		malformed = Operation{ID: "purge-victim", Kind: "purge", Phase: "prepared", Lifecycle: Deleted, SessionIDs: []string{"victim"}, ExpectedGeneration: status.Generation}
		state.PendingOperations[malformed.ID] = malformed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ClassifyPurge(state, "victim") != PurgeInvalid {
		t.Fatalf("malformed purge classification = %v", ClassifyPurge(state, "victim"))
	}
	if err := store.ResumePurge(ctx, "victim", malformed); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("malformed replay = %v, want conflict", err)
	}
	state, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.PendingOperations[malformed.ID]; !exists {
		t.Fatal("malformed purge evidence was removed")
	}
}

func TestPurgeIgnoresUnrelatedSessionGeneration(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"victim", "other"} {
		if err := store.AttachSession(ctx, "", GlobalWorkspaceID, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ArchiveSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	observed, _ := store.Load(ctx)
	if err := store.ArchiveSession(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginPurge(ctx, "victim", observed.Generation); err != nil {
		t.Fatalf("unrelated lifecycle change blocked purge: %v", err)
	}
}

func TestRestoreOperationCommitClearsLegacyPreparedPurge(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(ctx, "", GlobalWorkspaceID, "victim", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	if err := store.mutate(ctx, func(state *State) error {
		status := state.SessionStates["victim"]
		state.PendingOperations["purge-victim"] = Operation{ID: "purge-victim", Kind: "purge", Phase: "prepared", Lifecycle: Deleted, SessionIDs: []string{"victim"}, ExpectedGeneration: status.Generation}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load(ctx)
	op := Operation{ID: "restore_victim", Kind: "restore", Lifecycle: Active, WorkspaceID: GlobalWorkspaceID, SessionIDs: []string{"victim"}, ExpectedGeneration: state.Generation}
	if err := store.BeginOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOperationContent(ctx, op.ID, op.SessionIDs, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOperation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Load(ctx)
	if state.SessionStates["victim"].Lifecycle != Active || ClassifyPurge(state, "victim") != PurgeAbsent {
		t.Fatalf("restore commit left prepared purge: %+v", state)
	}
}

func TestV2UpgradePreservesV1EvidenceAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace-state-v1.json")
	root := t.TempDir()
	rootJSON, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"version":1,"generation":7,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","root":"/global","title":"Mine","visible":false,"sessionIds":["old"],"future":{"nested":42}}},"archivedSessionIds":["old"],"pendingCreates":{"reserved":{"operationId":"op","workspaceId":"global","sessionId":"reserved","future":true}},"futureRoot":{"keep":true}}`)
	original = []byte(strings.ReplaceAll(string(original), `"/global"`, string(rootJSON)))
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != SchemaVersion || state.SessionStates["old"].Lifecycle != Archived {
		t.Fatalf("upgrade: %+v", state)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != string(original) {
		t.Fatal("read-only load changed source")
	}
	if err := store.RestoreSession(t.Context(), "old"); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "upgrade-backups", "workspace-v1-*.json"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups: %v %v", backups, err)
	}
	saved, _ := os.ReadFile(backups[0])
	if string(saved) != string(original) {
		t.Fatal("backup differs from source")
	}
	body, _ := os.ReadFile(path)
	for _, field := range []string{`"futureRoot"`, `"nested":42`, `"future":true`} {
		if !strings.Contains(string(body), field) {
			t.Fatalf("lost unknown field %s: %s", field, body)
		}
	}
	state, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ArchivedSessionIDs) != 0 || !state.Workspaces["global"].Visible {
		t.Fatal("restore did not publish visible membership")
	}
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: "global", Root: root, Title: "Overwrite", Visible: false}); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Load(t.Context())
	if state.Workspaces["global"].Title != "Mine" || !state.Workspaces["global"].Visible {
		t.Fatal("repeated discovery reset user presentation")
	}
}

func TestV2BackupFailureLeavesV1Untouched(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	original := []byte(`{"version":1,"workspaceIds":[],"workspaces":{}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "upgrade-backups"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(path).EnsureWorkspace(t.Context(), Workspace{ID: "global"}); err == nil {
		t.Fatal("upgrade ignored backup failure")
	}
	body, _ := os.ReadFile(path)
	if string(body) != string(original) {
		t.Fatal("failed upgrade changed source")
	}
}

func TestV2RecoveryCommitIsAtomicAndIdempotent(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: "global", Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	entry := RecoveryEntry{ID: "entry", SourceKey: "source", Status: "pending", Format: "legacy-trash"}
	if err := store.RecordRecovery(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load(t.Context())
	op := Operation{ID: "restore", Kind: "restore", RecoveryEntryID: entry.ID, WorkspaceID: "global", Lifecycle: Active, ExpectedGeneration: state.Generation}
	if err := store.BeginOperation(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	mapping := SourceMapping{SourceKey: "source", Path: "/old", Format: "legacy", Fingerprint: "bytes", SessionID: "restored", WorkspaceID: "global"}
	if err := store.PrepareOperationContent(t.Context(), op.ID, []string{"restored"}, &mapping, nil); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Load(t.Context())
	if len(state.Workspaces["global"].SessionIDs) != 0 || state.RecoveryEntries["entry"].Status != "pending" {
		t.Fatal("prepared operation leaked visible session")
	}
	restarted := NewStore(store.Path())
	for range 2 {
		if err := restarted.CommitOperation(t.Context(), op.ID); err != nil {
			t.Fatal(err)
		}
	}
	state, _ = restarted.Load(t.Context())
	if len(state.Workspaces["global"].SessionIDs) != 1 || state.RecoveryEntries["entry"].Status != "restored" || state.SourceMappings["source"].SessionID != "restored" {
		t.Fatalf("incomplete commit: %+v", state)
	}
}

func TestV2ArchiveBatchFailureDoesNotChangeOtherSessions(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: "global"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(t.Context(), "", "global", "a", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLifecycle(t.Context(), []string{"a", "missing"}, Archived); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("error=%v", err)
	}
	state, _ := store.Load(t.Context())
	if state.SessionStates["a"].Lifecycle != Active {
		t.Fatal("partial archive committed")
	}
}

func TestV2RejectsUnknownLifecycleWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	body := []byte(`{"version":2,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","sessionIds":["a"]}},"sessionStates":{"a":{"lifecycle":"future"}}}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).Load(t.Context()); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error=%v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(body) {
		t.Fatal("unknown lifecycle rewritten")
	}
}

func TestV2NestedMetadataRoundTrip(t *testing.T) {
	var state State
	body := []byte(`{"version":2,"workspaceIds":[],"workspaces":{},"sessionStates":{"a":{"lifecycle":"archived","extension":{"a":1}}},"sourceMappings":{"s":{"sourceKey":"s","sessionId":"a","fingerprint":"f","extraField":true}},"pendingOperations":{"o":{"id":"o","phase":"prepared","lifecycle":"active","mapping":{"sourceKey":"s","sessionId":"a","fingerprint":"f","unknown":7},"extraOp":8}},"recoveryEntries":{"r":{"id":"r","sourceKey":"s","status":"pending","future":9}},"presentation":{"a":{"title":"keep","future":10}}}`)
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"extension":{"a":1}`, `"extraField":true`, `"unknown":7`, `"extraOp":8`, `"future":9`, `"future":10`} {
		if !strings.Contains(string(result), want) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestArchiveImportsPublishOnlyWithWholeBatch(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: "global"}); err != nil {
		t.Fatal(err)
	}
	child := Operation{ID: "import", Kind: "archive-import", WorkspaceID: "global", SessionIDs: []string{"a"}, Lifecycle: Archived}
	if err := store.BeginOperation(ctx, child); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOperationContent(ctx, child.ID, child.SessionIDs, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Load(ctx)
	if len(before.Workspaces["global"].SessionIDs) != 0 {
		t.Fatal("staged import leaked")
	}
	op := Operation{ID: "archive", Kind: "archive", Lifecycle: Archived, SessionIDs: []string{"a", "missing"}, Dependencies: []string{child.ID}}
	if err := store.BeginOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOperationContent(ctx, op.ID, op.SessionIDs, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOperation(ctx, op.ID); err == nil {
		t.Fatal("invalid member committed")
	}
	failed, _ := store.Load(ctx)
	if len(failed.Workspaces["global"].SessionIDs) != 0 || failed.PendingOperations[child.ID].Phase != "content_ready" {
		t.Fatal("partial commit")
	}
	op.ID, op.SessionIDs = "valid", []string{"a"}
	if err := store.BeginOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOperationContent(ctx, op.ID, op.SessionIDs, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOperation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	committed, _ := store.Load(ctx)
	if committed.SessionStates["a"].Lifecycle != Archived {
		t.Fatal("archive not published")
	}
	if err := store.CommitOperation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	retried, _ := store.Load(ctx)
	if retried.Generation != committed.Generation {
		t.Fatal("idempotent retry invalidated cursors")
	}
	if err := store.PrepareOperationContent(ctx, op.ID, []string{"different"}, nil, nil); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("retarget=%v", err)
	}
}

func TestV2MissingLifecycleIsNotReinterpretedAsActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","sessionIds":["a"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).Load(t.Context()); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("missing state=%v", err)
	}
}
