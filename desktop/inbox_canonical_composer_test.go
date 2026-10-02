package main

import "testing"

func TestManualSessionComposerGuidanceReceiptRecovery(t *testing.T) {
	a := newManualSessionTestApp(t)
	created, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: "canonical-guidance", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	a.manualCreationTasks.Wait()
	if copies, err := a.ListSessionComposerConflicts(created.Ref); err != nil || copies == nil || len(copies) != 0 {
		t.Fatalf("retired input copies must remain an empty compatibility response: %v %v", copies, err)
	}
	meta := a.metaForDraftSession(created.Ref.SessionID)
	if meta == nil || meta.Session == nil || meta.SessionPath != "" {
		t.Fatalf("formal session must not need a fabricated legacy path: %+v", meta)
	}
	_, ctrl := a.tabAndCtrlByID(meta.ID)
	if err := ctrl.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	saved, err := a.SaveSessionComposerState(SessionComposerSaveRequest{Ref: created.Ref, ExpectedRevision: "0", ContentVersion: 1, ContentJSON: `{"text":"queued guidance"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginSessionComposerSubmission(created.Ref, saved.Revision, "guidance-receipt", `{"kind":"guidance","display":"queued guidance","submit":"queued guidance"}`); err != nil {
		t.Fatal(err)
	}
	target, err := a.CaptureInboxTarget(meta.ID, sessionRoute(created.Ref.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := a.EnqueueInboxFollowupForTarget(target, "queued guidance", "queued guidance", nil, "guidance-receipt")
	if err != nil || receipt.ItemID == "" {
		t.Fatalf("guidance enqueue: %+v %v", receipt, err)
	}
	// Simulate a lost enqueue response. The read-only recovery must find the
	// canonical inbox receipt before there is any user row for this instruction.
	recovered, err := a.GetSessionComposerState(created.Ref)
	if err != nil || recovered.SubmissionID != "" || recovered.ContentJSON != "{}" {
		t.Fatalf("canonical guidance recovery: %+v %v", recovered, err)
	}
	if got := len(ctrl.InboxSnapshot().Items); got != 1 {
		t.Fatalf("recovery replayed guidance: %d items", got)
	}
}
