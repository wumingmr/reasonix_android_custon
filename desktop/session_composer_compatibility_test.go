package main

import (
	"testing"

	"reasonix/desktop/internal/sessionui"
	"reasonix/internal/provider"
)

func TestComposerReupgradeDetectsHistoryWithoutSubmissionReceipts(t *testing.T) {
	a, ref := lifecycleFixture(t)
	t.Cleanup(func() { _ = a.sessionUIStore().Close() })
	initial, err := a.GetSessionComposerState(ref)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := a.SaveSessionComposerState(SessionComposerSaveRequest{Ref: ref, ExpectedRevision: initial.Revision, ContentVersion: 1, ContentJSON: `{"text":"unsent before downgrade"}`})
	if err != nil {
		t.Fatal(err)
	}
	path := a.sessionUIStore().Path()
	if err := a.sessionUIStore().Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate an old/imported history writer that does not know session-ui or
	// attach a SubmissionID. The independent input file must remain unchanged.
	binding, err := a.desktopSessionService("").Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, binding.Runtime(), "old-writer", provider.Message{ID: "old-writer-user", Role: provider.RoleUser, Content: "continued in old version"})
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.sessionUI = sessionui.New(path)
	recovered, err := a.GetSessionComposerState(ref)
	if err != nil || !recovered.HistoryChanged || recovered.ContentJSON != saved.ContentJSON {
		t.Fatalf("unsafe reupgrade: %+v %v", recovered, err)
	}
	if _, err := a.BeginSessionComposerSubmission(ref, recovered.Revision, "must-not-send", `{}`); err == nil {
		t.Fatal("unreviewed old input became sendable")
	}
}

func TestComposerArchiveRestoreAndPurgeKeepLifecycleOwnership(t *testing.T) {
	a, ref := lifecycleFixture(t)
	t.Cleanup(func() { _ = a.sessionUIStore().Close() })
	input, err := a.SaveSessionComposerState(SessionComposerSaveRequest{Ref: ref, ExpectedRevision: "0", ContentVersion: 1, ContentJSON: `{"text":"archive input","attachments":[{"path":"missing-file"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ArchiveSessionTarget(SessionSelector{Ref: &ref}); err != nil {
		t.Fatal(err)
	}
	archived, err := a.GetSessionComposerState(ref)
	if err != nil || archived.ContentJSON != input.ContentJSON {
		t.Fatalf("archive lost input: %+v %v", archived, err)
	}
	if _, err := a.RestoreSessionTarget(SessionSelector{Ref: &ref}); err != nil {
		t.Fatal(err)
	}
	restored, err := a.GetSessionComposerState(ref)
	if err != nil || restored.ContentJSON != input.ContentJSON {
		t.Fatalf("restore lost input: %+v %v", restored, err)
	}
	if _, err := a.ArchiveSessionTarget(SessionSelector{Ref: &ref}); err != nil {
		t.Fatal(err)
	}
	if err := a.PurgeCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	row, err := a.sessionUIStore().Get(t.Context(), "composer", composerRecordKey(ref))
	if err != nil || row.Revision != "0" {
		t.Fatalf("purge retained input: %+v %v", row, err)
	}
	if _, err := a.SaveSessionComposerState(SessionComposerSaveRequest{Ref: ref, ExpectedRevision: "0", ContentVersion: 1, ContentJSON: `{"text":"late save"}`}); err == nil {
		t.Fatal("late save revived purged input")
	}
}

func TestComposerSubmissionIdentityCannotClearAnotherInputVersion(t *testing.T) {
	a, ref := lifecycleFixture(t)
	t.Cleanup(func() { _ = a.sessionUIStore().Close() })
	saved, err := a.SaveSessionComposerState(SessionComposerSaveRequest{Ref: ref, ExpectedRevision: "0", ContentVersion: 1, ContentJSON: `{"text":"first"}`})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := a.BeginSessionComposerSubmission(ref, saved.Revision, "fixed-submission", `{"submit":"first"}`)
	if err != nil {
		t.Fatal(err)
	}
	if repeated, err := a.BeginSessionComposerSubmission(ref, saved.Revision, "fixed-submission", `{"submit":"first"}`); err != nil || repeated.Revision != pending.Revision {
		t.Fatalf("identical retry changed association: %+v %v", repeated, err)
	}
	if _, err := a.BeginSessionComposerSubmission(ref, saved.Revision, "fixed-submission", `{"submit":"different"}`); err == nil {
		t.Fatal("changed request reused frozen identity")
	}
	done, err := a.CompleteSessionComposerSubmission(ref, "fixed-submission", "accepted")
	if err != nil {
		t.Fatal(err)
	}
	next, err := a.SaveSessionComposerState(SessionComposerSaveRequest{Ref: ref, ExpectedRevision: done.Revision, ContentVersion: 1, ContentJSON: `{"text":"second"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginSessionComposerSubmission(ref, next.Revision, "fixed-submission", `{"submit":"second"}`); err == nil {
		t.Fatal("old receipt can clear a new input")
	}
	late, err := a.CompleteSessionComposerSubmission(ref, "fixed-submission", "accepted")
	if err != nil || late.ContentJSON != next.ContentJSON {
		t.Fatalf("late acceptance cleared new input: %+v %v", late, err)
	}
}
