package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncatalog"
)

const recoveryChainLength = 9

func countDirs(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

// Recovery snapshots written by older builds chain each ParentID to the
// previous snapshot, so one action must follow the chain, not only the
// snapshots that share a parent.
func seedChainedRecoveryApp(t *testing.T) (*App, func(string) []ProjectNode, []ProjectNode) {
	t.Helper()
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	parent := filepath.Join(dir, "20260803-140947.000000000-fake-model.jsonl")
	p := agent.NewSession("system")
	p.Add(provider.Message{ID: "q0", Role: provider.RoleUser, Content: "long running conversation"})
	if err := p.Save(parent); err != nil {
		t.Fatal(err)
	}
	prev := parent
	for i := range recoveryChainLength {
		s := agent.NewSession("system")
		s.Add(provider.Message{ID: "q0", Role: provider.RoleUser, Content: "long running conversation"})
		s.Add(provider.Message{ID: "a", Role: provider.RoleAssistant, Content: strings.Repeat("x", i+1)})
		meta := agent.BranchMeta{Scope: "global", TopicID: "topic_chain", TopicTitle: "long running conversation", ParentID: agent.BranchID(prev)}
		info, err := s.SaveConflictRecoveryBranch(agent.RecoveryBranchOptions{OriginalPath: prev, Reason: "conflict", BranchMeta: meta})
		if err != nil {
			t.Fatal(err)
		}
		prev = info.Path
	}
	_ = os.Remove(parent)
	other := filepath.Join(dir, "20260826-120000.000000000-fake-model.jsonl")
	o := agent.NewSession("system")
	o.Add(provider.Message{ID: "q0", Role: provider.RoleUser, Content: "another conversation"})
	if err := o.Save(other); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveConflictRecoveryBranch(agent.RecoveryBranchOptions{OriginalPath: other, Reason: "conflict",
		BranchMeta: agent.BranchMeta{Scope: "global", TopicID: "topic_other", TopicTitle: "another conversation"}}); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	catalog, err := sessioncatalog.Open(context.Background(), sessioncatalog.Options{InMemory: true, DisableRepair: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() { app.stopSessionCatalog(time.Second) })
	rows := func(topic string) []ProjectNode {
		t.Helper()
		if err := catalog.ReconcileDirectory(context.Background(), sessioncatalog.DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
			t.Fatal(err)
		}
		page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		app.ReleaseReadSnapshot(page.SnapshotID)
		var out []ProjectNode
		for _, n := range page.Items {
			if n.Recovered && n.TopicID == topic {
				out = append(out, n)
			}
		}
		return out
	}
	before := rows("topic_chain")
	if len(before) != recoveryChainLength {
		t.Fatalf("recovered rows before archive = %d, want %d", len(before), recoveryChainLength)
	}
	return app, rows, before
}

func archiveRow(t *testing.T, app *App, n ProjectNode) SessionMutationResult {
	t.Helper()
	res, err := app.ArchiveSessionTarget(SessionSelector{Ref: n.Session, Source: n.Source, SessionPath: n.SessionPath})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestArchivingChainedRecoveryRowArchivesWholeLineageOnce(t *testing.T) {
	app, rows, before := seedChainedRecoveryApp(t)
	archive := func(n ProjectNode) {
		t.Helper()
		if _, err := app.ArchiveSessionTarget(SessionSelector{Ref: n.Session, Source: n.Source, SessionPath: n.SessionPath}); err != nil {
			t.Fatal(err)
		}
	}
	archive(before[0])
	if left := rows("topic_chain"); len(left) != 0 {
		t.Fatalf("one archive action left %d of %d chained recovery snapshots listed", len(left), recoveryChainLength)
	}
	store := app.desktopSessions.root
	imported := countDirs(t, store)
	if imported != recoveryChainLength {
		t.Fatalf("archived copies = %d, want one per snapshot (%d)", imported, recoveryChainLength)
	}
	archive(before[len(before)-1])
	if got := countDirs(t, store); got != imported {
		t.Fatalf("repeating the action wrote %d new session directories", got-imported)
	}
	if kept := rows("topic_other"); len(kept) != 1 {
		t.Fatalf("an unrelated recovered conversation was archived: %d rows left, want 1", len(kept))
	}
}

func TestArchivingRecoveryLineageReleasesRuntimeLockBetweenSnapshots(t *testing.T) {
	app, rows, before := seedChainedRecoveryApp(t)
	attempts, held := 0, 0
	app.runtimeMutationBeforeLockHook = func(string) {
		attempts++
		if !app.runtimeAdmissionMu.TryLock() {
			held++
			return
		}
		app.runtimeAdmissionMu.Unlock()
	}
	archiveRow(t, app, before[0])
	if attempts != recoveryChainLength {
		t.Fatalf("lock taken %d times, want once per snapshot (%d)", attempts, recoveryChainLength)
	}
	if held != 0 {
		t.Fatalf("runtime lock stayed held between %d snapshots", held)
	}
	if left := rows("topic_chain"); len(left) != 0 {
		t.Fatalf("%d snapshots left listed", len(left))
	}
}

func TestArchivingRecoveryLineageReportsPartialAndResumes(t *testing.T) {
	app, rows, before := seedChainedRecoveryApp(t)
	calls := 0
	app.runtimeMutationBeforeLockHook = func(string) {
		calls++
		switch calls {
		case 4:
			app.runtimeAdmissionMu.Lock()
		case 5:
			app.runtimeAdmissionMu.Unlock()
		}
	}
	res := archiveRow(t, app, before[0])
	if !res.Committed || res.Outcome != sessionOutcomeArchivedPartial || res.PendingSiblings != 1 {
		t.Fatalf("receipt = %+v, want committed archived_partial with 1 pending", res)
	}
	if left := rows("topic_chain"); len(left) != 1 {
		t.Fatalf("%d snapshots left after partial run, want 1", len(left))
	}
	app.runtimeMutationBeforeLockHook = nil
	again := archiveRow(t, app, rows("topic_chain")[0])
	if again.Outcome == sessionOutcomeArchivedPartial {
		t.Fatalf("retry still partial: %+v", again)
	}
	if left := rows("topic_chain"); len(left) != 0 {
		t.Fatalf("retry left %d snapshots", len(left))
	}
	if got := countDirs(t, app.desktopSessions.root); got != recoveryChainLength {
		t.Fatalf("archived copies = %d, want %d", got, recoveryChainLength)
	}
}
