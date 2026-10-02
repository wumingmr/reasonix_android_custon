package cli

import (
	"fmt"
	"strings"
	"testing"

	"reasonix/internal/i18n"
)

func TestRenameCurrentCanonicalSessionWritesTitleEvent(t *testing.T) {
	m, ctrl, service, _ := newCanonicalTakeoverTUI(t)

	m.runRenameCommand("/rename testname")

	out := strings.Join(m.transcript, "\n")
	if !strings.Contains(out, fmt.Sprintf(i18n.M.RenameDoneFmt, "testname")) {
		t.Fatalf("transcript missing rename confirmation:\n%s", out)
	}
	ref, _ := ctrl.SessionRef()
	snapshot, err := service.Query().Snapshot(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Projection.Title != "testname" {
		t.Fatalf("canonical title = %q, want testname", snapshot.Projection.Title)
	}
}

func TestRenameIndexedCanonicalSessionWritesTitleEvent(t *testing.T) {
	m, _, service, held := newCanonicalTakeoverTUI(t)
	if err := service.SetTitle(t.Context(), held, "before"); err != nil {
		t.Fatal(err)
	}
	index := 0
	for i, entry := range mergedResumeEntries(m.ctrl.SessionDir(), resumeListCap) {
		if entry.target.ref == held {
			index = i + 1
		}
	}
	if index == 0 {
		t.Fatal("held session missing from the resume list")
	}

	m.runRenameCommand(fmt.Sprintf("/rename %d after rename", index))

	out := strings.Join(m.transcript, "\n")
	if !strings.Contains(out, fmt.Sprintf(i18n.M.RenameDoneFmt, "after rename")) {
		t.Fatalf("transcript missing rename confirmation:\n%s", out)
	}
	snapshot, err := service.Query().Snapshot(t.Context(), held)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Projection.Title != "after rename" {
		t.Fatalf("canonical title = %q, want %q", snapshot.Projection.Title, "after rename")
	}
}
