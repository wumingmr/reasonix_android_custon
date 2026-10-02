package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func forkLegacyVersion(t *testing.T, legacy *agent.Session, path, kind, name, answer string) string {
	t.Helper()
	head, err := legacy.ForkHead(path, "question", kind, name)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{Role: provider.RoleAssistant, Content: answer})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	return head
}

func legacyVersionApp(t *testing.T, path string) (*App, string, func() map[string]bool) {
	t.Helper()
	app := NewApp()
	app.ctx = t.Context()
	root := pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	installSessionCatalogForTest(t, app, filepath.Dir(path), "global", "")
	return app, root, func() map[string]bool {
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
}

func countSessionDirs(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, entry := range entries {
		if entry.IsDir() {
			dirs++
		}
	}
	return dirs
}

// Before rewinds became their own head kind, "fork from this turn" wrote an
// unnamed fork head and a second writer's save a concurrent one; both keep the
// conversation. Only a named fork is one the user split off.
func TestArchivingLegacyVersionRowCoversUnnamedForkAndConcurrentVersions(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, mainHead := migrationSingleDAGFixture(t)
	legacy.Add(provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "first answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	first := forkLegacyVersion(t, legacy, path, agent.HeadKindFork, "", "second answer")
	second := forkLegacyVersion(t, legacy, path, agent.HeadKindFork, "", "third answer")
	other, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{Role: provider.RoleUser, Content: "follow-up here"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	other.Add(provider.Message{Role: provider.RoleUser, Content: "follow-up elsewhere"})
	if err := other.Save(path); err != nil {
		t.Fatal(err)
	}
	concurrent, _ := other.Head()
	rewound := forkLegacyVersion(t, other, path, agent.HeadKindRewind, "", "answer after the concurrent write")
	named := forkLegacyVersion(t, legacy, path, agent.HeadKindFork, "separate idea", "forked answer")
	heads, err := agent.ListSessionHeads(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, head := range heads {
		if head.ID == concurrent.HeadID && head.Kind != agent.HeadKindConcurrent {
			t.Fatalf("fixture head %s is %q, want a concurrent head", head.ID, head.Kind)
		}
	}
	versions := []string{mainHead, first, second, rewound}

	app, root, rowHeads := legacyVersionApp(t, path)
	if got := rowHeads(); len(got) != len(versions)+1 || !got[named] {
		t.Fatalf("fixture rows = %v; want %d versions and the named fork", got, len(versions))
	}
	selector := SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: second}}
	dirs := 0
	for attempt := range 2 {
		result, err := app.ArchiveSessionTarget(selector)
		if err != nil || !result.Committed {
			t.Fatalf("archive %d = %+v, %v", attempt, result, err)
		}
		if got := rowHeads(); len(got) != 1 || !got[named] {
			t.Fatalf("archive %d left rows %v; want only the named fork", attempt, got)
		}
		state, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		mapped := map[string]string{}
		for _, mapping := range state.SourceMappings {
			if previous, dup := mapped[mapping.HeadID]; dup {
				t.Fatalf("head %s materialized twice: %s and %s", mapping.HeadID, previous, mapping.SessionID)
			}
			mapped[mapping.HeadID] = mapping.SessionID
			if lifecycle := state.SessionStates[mapping.SessionID].Lifecycle; lifecycle != workspacestate.Archived {
				t.Fatalf("version %s is %v, not archived", mapping.HeadID, lifecycle)
			}
		}
		if len(mapped) != len(versions) {
			t.Fatalf("archive %d mapped %v; want exactly the versions %v", attempt, mapped, versions)
		}
		for _, head := range versions {
			if _, ok := mapped[head]; !ok {
				t.Fatalf("version %s was not archived: %v", head, mapped)
			}
		}
		if attempt == 0 {
			dirs = countSessionDirs(t, root)
		} else if got := countSessionDirs(t, root); got != dirs {
			t.Fatalf("archive %d wrote new session storage: %d entries, was %d", attempt, got, dirs)
		}
	}
}

// Installs that archived versions one row at a time keep those records. When
// the transcript has changed since, a later archive of the conversation must
// retire the remaining rows without materializing the archived ones again.
func TestArchivingLegacyConversationDoesNotRematerializeArchivedVersions(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, mainHead := migrationSingleDAGFixture(t)
	legacy.Add(provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "first answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	versions := []string{mainHead}
	for _, retry := range []string{"second answer", "third answer", "fourth answer"} {
		versions = append(versions, forkLegacyVersion(t, legacy, path, agent.HeadKindRewind, "", retry))
	}
	named := forkLegacyVersion(t, legacy, path, agent.HeadKindFork, "separate idea", "forked answer")
	app, root, rowHeads := legacyVersionApp(t, path)

	for _, head := range versions[:2] {
		ctx, done, err := app.beginHistoricalRecovery()
		if err != nil {
			t.Fatal(err)
		}
		id, source, err := app.historicalSourceForSelector(SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: head}})
		if err != nil {
			done()
			t.Fatal(err)
		}
		_, err = app.archiveHistoricalSourceWithOperation(ctx, id, source, "archive-source-single-"+head)
		done()
		if err != nil {
			t.Fatal(err)
		}
	}
	legacy.Add(provider.Message{Role: provider.RoleUser, Content: "the named fork continued"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	before := countSessionDirs(t, root)

	result, err := app.ArchiveSessionTarget(SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: versions[3]}})
	if err != nil || !result.Committed {
		t.Fatalf("archive = %+v, %v", result, err)
	}
	if got := rowHeads(); len(got) != 1 || !got[named] {
		t.Fatalf("rows %v; want only the named fork", got)
	}
	if got := countSessionDirs(t, root); got != before+2 {
		t.Fatalf("archive wrote %d session directories; want 2, one per unarchived version", got-before)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	perHead := map[string]int{}
	for _, mapping := range state.SourceMappings {
		perHead[mapping.HeadID]++
	}
	for _, head := range versions {
		if perHead[head] != 1 {
			t.Fatalf("version %s has %d mappings; want 1 (%v)", head, perHead[head], perHead)
		}
	}
}

func TestArchivingLegacyVersionKeepsAnotherOpenVersionActive(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, mainHead := migrationSingleDAGFixture(t)
	legacy.Add(provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "first answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	otherHead := forkLegacyVersion(t, legacy, path, agent.HeadKindRewind, "", "second answer")
	app, root, _ := legacyVersionApp(t, path)

	prepared, err := app.PrepareSession(SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: mainHead}})
	if err != nil {
		t.Fatal(err)
	}
	app.historicalImports.mu.Lock()
	call := app.historicalImports.operations[prepared.OperationID]
	app.historicalImports.mu.Unlock()
	imported, err := waitHistoricalImport(call)
	if err != nil {
		t.Fatal(err)
	}
	active := session.SessionRef{HostID: localDesktopHostID, SessionID: imported.Session.SessionID}
	if _, err := app.OpenSession(active); err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{Role: provider.RoleUser, Content: "the other legacy version continued"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	before := countSessionDirs(t, root)

	result, err := app.ArchiveSessionTarget(SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: otherHead}})
	if err != nil || !result.Committed {
		t.Fatalf("archive other version = %+v, %v", result, err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.SessionStates[active.SessionID].Lifecycle; got != workspacestate.Active {
		t.Fatalf("open version lifecycle = %q, want active", got)
	}
	mainMappings := 0
	for _, mapping := range state.SourceMappings {
		if mapping.HeadID == mainHead {
			mainMappings++
			if mapping.SessionID != active.SessionID {
				t.Fatalf("open version remapped from %s to %s", active.SessionID, mapping.SessionID)
			}
		}
	}
	if mainMappings != 1 {
		t.Fatalf("open version has %d mappings, want one", mainMappings)
	}
	if got := countSessionDirs(t, root); got != before+1 {
		t.Fatalf("archive wrote %d session directories, want one for the other version", got-before)
	}
	if sourceAfter, err := desktopSourceFingerprint(path); err != nil || sourceAfter != sourceBefore {
		t.Fatalf("archive changed the legacy source: before=%s after=%s err=%v", sourceBefore, sourceAfter, err)
	}
}
