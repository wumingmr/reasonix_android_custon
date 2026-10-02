package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/identitylock"
	"reasonix/internal/session"
)

func historicalArchiveFixture(t *testing.T) (*App, SessionSelector, *session.Service) {
	t.Helper()
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	old := coldV4MigrationFixture(t, root, "archive-transaction")
	app := newHistoricalLifecycleApp(t)
	key := historicalLifecycleID(t, app, "archive-transaction")
	return app, SessionSelector{Source: &SessionSourceRef{Path: filepath.Join(root, "archive-transaction"), SourceKey: key}}, old
}

func TestHistoricalArchiveTransactionRestart(t *testing.T) {
	t.Run("conflicting-content-ready", func(t *testing.T) { checkConflictingArchiveReservation(t, false) })
	t.Run("identical-content-ready", func(t *testing.T) { checkConflictingArchiveReservation(t, true) })
	for _, stage := range []string{"content-published", "before-archive-commit", "unfinished-import"} {
		t.Run(stage, func(t *testing.T) {
			app, selector, _ := historicalArchiveFixture(t)
			before := migrationSourceSnapshot(t, canonicalMigrationSourceFiles(config.SessionStoreDir(), "archive-transaction"))
			if stage == "before-archive-commit" {
				app.lifecycleCheckpointHook = func(phase string) {
					if phase == stage {
						panic("simulated exit")
					}
				}
				func() {
					defer func() {
						if recover() == nil {
							t.Error("missing interruption")
						}
					}()
					_, _ = app.ArchiveSessionTarget(selector)
				}()
			} else {
				app.desktopSessions.beforeMigrationRegistryCommit = func() error { return errors.New("simulated exit") }
				var err error
				if stage == "unfinished-import" {
					_, err = app.ImportHistoricalSession(selector.Source.SourceKey)
				} else {
					_, err = app.ArchiveSessionTarget(selector)
				}
				if err == nil {
					t.Fatal("missing interruption")
				}
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(state.SourceMappings) != 0 || len(state.SessionStates) != 0 {
				t.Fatal("staged archive was published as an active session")
			}
			if stage == "before-archive-commit" {
				if _, err := app.ImportHistoricalSession(selector.Source.SourceKey); !historicalSourceBusyError(err) {
					t.Fatalf("navigation stole an archive reservation: %v", err)
				}
			}
			app.stopHistoricalImports()
			app.closeSessionServices()
			app = newHistoricalLifecycleApp(t)
			result, err := app.ArchiveSessionTarget(selector)
			if err != nil || !result.Committed {
				t.Fatalf("resume: %+v, %v", result, err)
			}
			if err := app.recoverDesktopOperations(t.Context(), false); err != nil {
				t.Fatal(err)
			}
			state, err = app.workspaceRegistry().Load(t.Context())
			if err != nil || len(state.SessionStates) != 1 || len(state.SourceMappings) != 1 {
				t.Fatalf("duplicate or missing archive: %+v, %v", state.SessionStates, err)
			}
			for _, status := range state.SessionStates {
				if status.Lifecycle != workspacestate.Archived {
					t.Fatalf("unexpected lifecycle: %+v", status)
				}
			}
			assertMigrationSourceSnapshot(t, before)
		})
	}
}

func checkConflictingArchiveReservation(t *testing.T, identical bool) {
	app, selector, old := historicalArchiveFixture(t)
	ctx := t.Context()
	base, err := app.ImportHistoricalSession(selector.Source.SourceKey)
	if err != nil {
		t.Fatal(err)
	}
	oldRef := session.SessionRef{HostID: "migration-source", SessionID: filepath.Base(selector.Source.Path)}
	if identical {
		if err := old.SetTitle(ctx, oldRef, "metadata only"); err != nil {
			t.Fatal(err)
		}
	} else {
		appendMigrationTestMessage(t, old, oldRef, "independent later work")
	}
	if err := app.ArchiveCanonicalSession(base.Session); err != nil {
		t.Fatal(err)
	}
	if err := app.PurgeCanonicalSession(base.Session); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := desktopSourceFingerprint(selector.Source.Path)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "export")
	if err := old.TryExportCold(ctx, oldRef, bundle); err != nil {
		t.Fatal(err)
	}
	const reserved = "old-unversioned-reservation"
	if _, err := app.desktopSessionService("").ImportWithHeader(ctx, bundle, session.CreateOptions{SessionID: reserved, CWD: globalWorkspaceRoot(), Origin: session.SessionOriginCanonicalImport}); err != nil {
		t.Fatal(err)
	}
	if err := app.commitDesktopImport(ctx, desktopMigrationSource{scope: "global"}, selector.Source.Path, "canonical", fingerprint, reserved, base.WorkspaceID); !errors.Is(err, workspacestate.ErrMutationConflict) {
		t.Fatalf("fixture admission: %v", err)
	}
	state, err := app.workspaceRegistry().Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingHistoricalOperation(state, selector.Source.SourceKey)
	if pending == nil || pending.Phase != "content_ready" {
		t.Fatal("missing interrupted import")
	}
	app.stopHistoricalImports()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	result, err := app.ArchiveSessionTarget(selector)
	if err != nil || !result.Committed {
		t.Fatalf("archive interrupted conversion: %+v, %v", result, err)
	}
	state, err = app.workspaceRegistry().Load(ctx)
	if err != nil || state.SessionStates[base.Session.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatalf("original tombstone changed: %v", err)
	}
	if identical {
		if result.Outcome != "already_removed" || len(state.SessionStates) != 1 {
			t.Fatalf("identical staged copy was resurrected: %+v", result)
		}
	} else if state.PendingOperations[pending.ID].Phase != "committed" || state.SessionStates[reserved].Lifecycle != workspacestate.Archived || len(state.SessionStates) != 2 {
		t.Fatalf("reservation was duplicated or not completed: %+v", state.PendingOperations[pending.ID])
	}
}

func TestHistoricalArchiveRejectsWriterAndSourceChange(t *testing.T) {
	app, selector, _ := historicalArchiveFixture(t)
	release, err := identitylock.TryAcquire(filepath.Join(selector.Source.Path, "writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.ArchiveSessionTarget(selector)
	release()
	if err == nil || result.Committed {
		t.Fatal("archive ignored source writer")
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.PendingOperations) != 0 {
		t.Fatal("busy source admitted an operation")
	}
	// A source change outside the cooperative writer protocol is still caught
	// by the fingerprint fence before the parent publishes its staged content.
	app.desktopSessions.beforeMigrationRegistryCommit = func() error {
		path := filepath.Join(selector.Source.Path, "manifest.json")
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, append(body, '\n'), 0600)
	}
	if result, err := app.ArchiveSessionTarget(selector); err == nil || result.Committed {
		t.Fatal("failed validation was reported committed")
	}
}

func TestHistoricalArchiveDiscoversLaterWorkWithoutDuplicateMetadataCopy(t *testing.T) {
	app, selector, old := historicalArchiveFixture(t)
	first, err := app.ArchiveSessionTarget(selector)
	if err != nil {
		t.Fatal(err)
	}
	oldRef := session.SessionRef{HostID: "migration-source", SessionID: "archive-transaction"}
	if err := old.SetTitle(t.Context(), oldRef, "New title only"); err != nil {
		t.Fatal(err)
	}
	second, err := app.ArchiveSessionTarget(selector)
	if err != nil || second.TargetKey != first.TargetKey {
		t.Fatalf("metadata created a copy: %+v, %v", second, err)
	}
	appendMigrationTestMessage(t, old, oldRef, "later independent work")
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rows := app.historicalCanonicalTopicsFromProjection("global", "", state, workspacestate.NewWorkspaceIndex(state))
	if len(rows) != 0 {
		t.Fatalf("an adopted source must not return as a second ordinary conversation: %+v", rows)
	}
	third, err := app.ArchiveSessionTarget(selector)
	if err != nil || third.TargetKey == first.TargetKey || third.Outcome != "archived_copy" {
		t.Fatalf("later work: %+v, %v", third, err)
	}
	if err := old.SetTitle(t.Context(), oldRef, "Another metadata change"); err != nil {
		t.Fatal(err)
	}
	fourth, err := app.ArchiveSessionTarget(selector)
	if err != nil || fourth.TargetKey != third.TargetKey {
		t.Fatalf("metadata duplicated the version: %+v, %v", fourth, err)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.SessionStates) != 2 {
		t.Fatalf("expected two distinct histories: %+v, %v", state.SessionStates, err)
	}
}

func TestHistoricalArchiveTopicUsesSourceTransaction(t *testing.T) {
	app, selector, _ := historicalArchiveFixture(t)
	if err := app.saveHistoricalSourcePresentation(selector.Source.SourceKey, func(p *historicalSourcePresentation) {
		pinned := true
		p.Title, p.Pinned = "User's historical title", &pinned
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.TrashTopic("historical-" + selector.Source.SourceKey); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping, ok, err := state.ResolveSource(selector.Source.SourceKey)
	if err != nil || !ok || state.SessionStates[mapping.SessionID].Lifecycle != workspacestate.Archived {
		t.Fatalf("topic did not archive source: %v", err)
	}
	if !state.Presentation[mapping.SessionID].Pinned {
		t.Fatal("archive lost the historical pin")
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	if err := app.RestoreCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	info, err := app.desktopSessionService("").Query().Stat(t.Context(), ref)
	if err != nil || info.Title != "User's historical title" {
		t.Fatalf("restore lost the historical title: %+v, %v", info, err)
	}
}

func TestHistoricalArchiveDoesNotInferOriginFromDeletedID(t *testing.T) {
	app, selector, old := historicalArchiveFixture(t)
	ctx := t.Context()
	id := filepath.Base(selector.Source.Path)
	workspace, err := app.ensureDesktopWorkspace(ctx, "global", "")
	if err != nil {
		t.Fatal(err)
	}
	registry := app.workspaceRegistry()
	if err := registry.AttachSession(ctx, "", workspace, id, ""); err != nil {
		t.Fatal(err)
	}
	if err := registry.ArchiveSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	state, err := registry.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.BeginPurge(ctx, id, state.Generation); err != nil {
		t.Fatal(err)
	}
	digest, err := canonicalMigrationDigest(ctx, old.Query(), session.SessionRef{HostID: "migration-source", SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := desktopSourceFingerprint(selector.Source.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = app.resolveDesktopImportTarget(ctx, app.desktopSessionService("").Query(), id, "ledger-key", selector.Source.SourceKey, digest, selector.Source.Path, fingerprint)
	var operationErr *SessionOperationError
	if !errors.As(err, &operationErr) || operationErr.Code != "source_ambiguous" {
		t.Fatalf("unproven deletion was bypassed: %v", err)
	}
}

func TestHistoricalArchiveReceiptPreservesIndependentHeads(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	dir := filepath.Join(root, "head-copy")
	legacy := filepath.Join(config.SessionDir(), "heads.jsonl")
	writeMigrationJSON(t, filepath.Join(dir, "manifest.json"), session.Manifest{Codec: session.Codec, SessionID: "head-copy", Source: &session.Source{Path: legacy, Version: "legacy", LegacyHeadID: "other"}})
	base := desktopLegacyMigrationKey(legacy)
	if _, err := saveDesktopMigrationHeads(base, []string{"main", "other"}, "other", "revision", []desktopMigrationConversion{{Root: root, SessionID: "head-copy", HeadID: "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := updateDesktopMigrationLedger(base, "primary-target", "completed", "", "same-history"); err != nil {
		t.Fatal(err)
	}
	if err := updateDesktopMigrationLedger(desktopLegacyHeadKey(legacy, "other"), "independent-target", "completed", "", "same-history"); err != nil {
		t.Fatal(err)
	}
	receipts, err := canonicalConversionReceipts(t.Context(), dir)
	if err != nil || len(receipts) == 0 {
		t.Fatalf("head receipts: %+v, %v", receipts, err)
	}
	for _, receipt := range receipts {
		if receipt.TargetSessionID != "independent-target" {
			t.Fatalf("primary head consumed an independent head: %+v", receipt)
		}
	}
}
