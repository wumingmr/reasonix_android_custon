package main

import (
	"testing"

	"reasonix/desktop/internal/draftstate"
)

func TestTaggedDraftRestartReconcilesWithoutReplay(t *testing.T) {
	for _, version := range []string{"main-v2-pr10469", "main-v2-pr10572", "1.38.11"} {
		t.Run(version, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path := copyRollbackFixture(t, version, "drafts.sqlite")
			for restart := range 3 {
				app := NewApp()
				app.ctx = t.Context()
				app.desktopDrafts = draftstate.New(path)
				t.Cleanup(func() { _ = app.desktopDrafts.Close() })
				t.Cleanup(app.closeSessionServices)
				app.markTabsRestored()
				app.reconcileDraftSubmissionOperations()
				for original, want := range map[string]string{
					"reserved": "resume_required", "starting": "resume_required", "resume_required": "resume_required",
					"dispatching": "dispatch_unknown", "dispatching_shell": "dispatch_unknown", "dispatch_unknown": "dispatch_unknown",
					"accepted": "accepted", "cancel_requested": "cancelled", "cancelled": "cancelled", "failed": "dispatch_unknown",
				} {
					op, err := app.desktopDrafts.Operation(t.Context(), "operation-"+original)
					if err != nil || op.Phase != want || op.SessionID != "session-"+original || op.TopicID != "topic-"+original || op.SubmissionID != "submit-"+original {
						t.Fatalf("restart %d, %s: %+v %v", restart, original, op, err)
					}
				}
				accepted, err := app.desktopDrafts.Get(t.Context(), "draft-accepted")
				if err != nil || accepted.Status != "converted" {
					t.Fatalf("accepted conversion missing: %+v %v", accepted, err)
				}
				summaries, err := app.ListSessionDraftSummaries()
				if err != nil {
					t.Fatal(err)
				}
				settingsOnly := false
				for _, summary := range summaries {
					settingsOnly = settingsOnly || summary.ID == "draft-settings-only"
				}
				if !settingsOnly || len(app.ListTabs()) != 0 {
					t.Fatal("recovery lost settings-only draft or started a runtime")
				}
				infos, err := listAllCanonicalSessionInfo(t.Context(), app.desktopSessionService("").Query())
				if err != nil || len(infos) != 0 {
					t.Fatalf("reconciliation allocated a replacement session: %d %v", len(infos), err)
				}
				app.closeSessionServices()
				if err := app.desktopDrafts.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
