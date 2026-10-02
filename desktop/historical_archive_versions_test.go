package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

// Every rewind keeps the head it left as a version, and the sidebar lists each
// version of a legacy transcript as its own row with the same title. Archiving
// one of them archives the conversation, once, however often it is repeated.
func TestArchivingLegacyVersionRowArchivesEveryVersionOnce(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, mainHead := migrationSingleDAGFixture(t)
	legacy.Add(provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "first answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	versions := []string{mainHead}
	for _, retry := range []string{"second answer", "third answer", "fourth answer"} {
		head, err := legacy.ForkHead(path, "question", agent.HeadKindRewind, "")
		if err != nil {
			t.Fatal(err)
		}
		legacy.Add(provider.Message{Role: provider.RoleAssistant, Content: retry})
		if err := legacy.Save(path); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, head)
	}
	fork, err := legacy.ForkHead(path, "question", agent.HeadKindFork, "separate idea")
	if err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{Role: provider.RoleAssistant, Content: "forked answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.ctx = t.Context()
	root := pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	installSessionCatalogForTest(t, app, filepath.Dir(path), "global", "")
	rowHeads := func() map[string]bool {
		t.Helper()
		page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		heads := map[string]bool{}
		for _, row := range page.Items {
			if row.Source == nil {
				t.Fatalf("unexpected materialized row: %+v", row)
			}
			heads[row.Source.HeadID] = true
		}
		return heads
	}
	if got := rowHeads(); len(got) != len(versions)+1 || !got[fork] {
		t.Fatalf("fixture rows = %v; want %d versions and the fork", got, len(versions))
	}

	selector := SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: versions[2]}}
	var dirs int
	for attempt := range 3 {
		result, err := app.ArchiveSessionTarget(selector)
		if err != nil || !result.Committed {
			t.Fatalf("archive %d = %+v, %v", attempt, result, err)
		}
		if got := rowHeads(); len(got) != 1 || !got[fork] {
			t.Fatalf("archive %d left rows %v; want only the fork", attempt, got)
		}
		state, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(state.SourceMappings) != len(versions) || len(state.SessionStates) != len(versions) {
			t.Fatalf("archive %d: %d mappings, %d sessions; want one per version (%d)",
				attempt, len(state.SourceMappings), len(state.SessionStates), len(versions))
		}
		mapped := map[string]string{}
		for _, mapping := range state.SourceMappings {
			if !sameDesktopPath(mapping.Path, path) {
				t.Fatalf("mapping for another source: %+v", mapping)
			}
			if previous, dup := mapped[mapping.HeadID]; dup {
				t.Fatalf("head %s materialized twice: %s and %s", mapping.HeadID, previous, mapping.SessionID)
			}
			mapped[mapping.HeadID] = mapping.SessionID
			if lifecycle := state.SessionStates[mapping.SessionID].Lifecycle; lifecycle != workspacestate.Archived {
				t.Fatalf("version %s is %v, not archived", mapping.HeadID, lifecycle)
			}
		}
		for _, head := range versions {
			if _, ok := mapped[head]; !ok {
				t.Fatalf("version %s was not archived: %v", head, mapped)
			}
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			dirs = len(entries)
		} else if len(entries) != dirs {
			t.Fatalf("archive %d wrote new session storage: %d entries, was %d", attempt, len(entries), dirs)
		}
	}
	if after, err := desktopSourceFingerprint(path); err != nil || after != before {
		t.Fatalf("archive changed the legacy source: %v", err)
	}
}
