package main

import "testing"

func TestDraftHasContentIgnoresClearedComposerState(t *testing.T) {
	cleared := `{"text":"  ","invocations":[],"attachments":[],"workspaceRefs":[],"pastedBlocks":[],"openPastedLabels":["kept ui state"],"sessionRefs":[],"selectedTextRefs":[]}`
	for _, empty := range []string{"", "{}", "  {}  ", cleared} {
		if draftHasContent(empty) {
			t.Fatalf("draftHasContent(%q) = true, want false", empty)
		}
	}
	for _, filled := range []string{
		`{"text":"hello"}`,
		`{"goalDraft":true}`,
		`{"text":"","attachments":[{"path":"a.png"}]}`,
		`{"text":"","workspaceRefs":[{"path":"src"}]}`,
		`{"text":"","invocations":[{"name":"x"}]}`,
		`{"text":"","pastedBlocks":[{"label":"p"}]}`,
		`{"text":"","sessionRefs":[{"id":"s"}]}`,
		`{"text":"","selectedTextRefs":[{"text":"t"}]}`,
		`not json`,
	} {
		if !draftHasContent(filled) {
			t.Fatalf("draftHasContent(%q) = false, want true", filled)
		}
	}
}

func TestListSessionDraftSummariesReportsContentFromFields(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := newDraftTestApp(t)
	draft, err := a.seedPreviousDraftForTarget("global", "")
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := a.ListSessionDraftSummaries()
	if err != nil || len(summaries) != 1 || summaries[0].HasContent {
		t.Fatalf("fresh draft summaries = %+v, %v", summaries, err)
	}
	saved, err := a.SaveSessionDraft(SessionDraftSaveRequest{DraftID: draft.ID, Revision: draft.Revision,
		ContentJSON: `{"text":"draft text","invocations":[],"attachments":[]}`, Settings: draft.Settings})
	if err != nil || saved.Outcome != "saved" {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	summaries, err = a.ListSessionDraftSummaries()
	if err != nil || len(summaries) != 1 || !summaries[0].HasContent {
		t.Fatalf("filled draft summaries = %+v, %v", summaries, err)
	}
	cleared, err := a.SaveSessionDraft(SessionDraftSaveRequest{DraftID: draft.ID, Revision: saved.Draft.Revision,
		ContentJSON: `{"text":"","invocations":[],"attachments":[],"workspaceRefs":[],"pastedBlocks":[],"openPastedLabels":[],"sessionRefs":[],"selectedTextRefs":[]}`,
		Settings:    draft.Settings})
	if err != nil || cleared.Outcome != "saved" {
		t.Fatalf("clear = %+v, %v", cleared, err)
	}
	summaries, err = a.ListSessionDraftSummaries()
	if err != nil || len(summaries) != 1 || summaries[0].HasContent {
		t.Fatalf("cleared draft summaries = %+v, %v", summaries, err)
	}
}
