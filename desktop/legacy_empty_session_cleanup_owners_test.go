package main

import (
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

func TestLegacyCleanupProtectsRegistryRecoveryOwners(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		reason string
		own    func(*testing.T, *App, session.SessionRef, string, string)
	}{
		{
			name: "recovery entry", reason: "recovery_owner",
			own: func(t *testing.T, app *App, ref session.SessionRef, workspaceID, workspaceRoot string) {
				t.Helper()
				if err := app.workspaceRegistry().RecordRecovery(t.Context(), workspacestate.RecoveryEntry{
					ID: "cleanup-recovery", SourceKey: "cleanup-source", SessionID: ref.SessionID, WorkspaceID: workspaceID,
					Scope: "project", WorkspaceRoot: workspaceRoot, Format: "canonical", Reason: "interrupted", Status: "pending",
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "pending operation", reason: "pending_operation",
			own: func(t *testing.T, app *App, ref session.SessionRef, workspaceID, _ string) {
				t.Helper()
				if err := app.workspaceRegistry().BeginOperation(t.Context(), workspacestate.Operation{
					ID: "cleanup-operation", Kind: "command", Lifecycle: workspacestate.Active,
					SessionIDs: []string{ref.SessionID}, WorkspaceID: workspaceID,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			app, workspaceRoot := newLegacyCleanupTestApp(t)
			ref, workspaceID := createLegacyCleanupSession(t, app, workspaceRoot, "legacy-registry-owner", false)
			fixture.own(t, app, ref, workspaceID, workspaceRoot)
			if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
				t.Fatal(err)
			}
			app.processLegacyCleanupSession(cleanupCandidate(t, app, "session:"+ref.SessionID))
			if got := cleanupCandidate(t, app, "session:"+ref.SessionID); got.Phase != "protected" || got.Reason != fixture.reason {
				t.Fatalf("registry-owned candidate = %+v", got)
			}
		})
	}
}
