package control

import (
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

func newSchemaTwoBranchController(t *testing.T) (*Controller, *agent.Session, string) {
	t.Helper()
	dir := t.TempDir()
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	sess := exec.Session()
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "root prompt"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "root answer"})
	c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, Label: "test", Sink: event.Discard})
	path := filepath.Join(dir, "root.jsonl")
	c.SetSessionPath(path)
	if err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if _, ok := sess.Head(); !ok {
		t.Fatal("session must be schema 2 after its first save")
	}
	return c, sess, path
}

func TestBranchAndSwitchUseIndependentSessionsForSchemaTwo(t *testing.T) {
	c, _, path := newSchemaTwoBranchController(t)
	rootID := agent.BranchID(path)
	branchPath, err := c.Branch("experiment")
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	if branchPath == "" || branchPath == path || !strings.HasSuffix(branchPath, ".jsonl") {
		t.Fatalf("Branch must return an independent session path, got %q", branchPath)
	}
	if c.SessionPath() != branchPath {
		t.Fatalf("branch must switch to the child session: %q", c.SessionPath())
	}
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.jsonl"))
	transcripts := 0
	for _, entry := range entries {
		if store.IsSessionTranscriptName(filepath.Base(entry)) {
			transcripts++
		}
	}
	if transcripts != 2 {
		t.Fatalf("branch transcripts = %v", entries)
	}
	c.executor.Session().Add(provider.Message{Role: provider.RoleUser, Content: "on the branch"})
	if err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	branches, err := c.Branches()
	if err != nil {
		t.Fatal(err)
	}
	var main, branch *agent.BranchInfo
	for i := range branches {
		switch branches[i].ID {
		case rootID:
			main = &branches[i]
		case agent.BranchID(branchPath):
			branch = &branches[i]
		}
	}
	if main == nil || branch == nil {
		t.Fatalf("branches = %+v, want parent and independent child", branches)
	}
	if branch.ParentID != rootID || branch.Name != "experiment" || branch.Path != branchPath || branch.HeadID != "" {
		t.Fatalf("file branch infos = main %+v branch %+v", main, branch)
	}
	tree := c.BranchTreeText()
	if !strings.Contains(tree, "experiment") {
		t.Fatalf("tree must list the head:\n%s", tree)
	}
	if _, err := c.SwitchBranch(rootID); err != nil {
		t.Fatalf("SwitchBranch main: %v", err)
	}
	if got := len(c.executor.Session().Snapshot()); got != 3 || c.SessionPath() != path {
		t.Fatalf("after switching back: %d messages path %q", got, c.SessionPath())
	}
	if _, err := c.SwitchBranch(agent.BranchID(branchPath)); err != nil {
		t.Fatalf("SwitchBranch child: %v", err)
	}
	if got := c.executor.Session().Snapshot(); len(got) != 4 || got[3].Content != "on the branch" {
		t.Fatalf("after switching to the child: %+v", got)
	}
	reloaded, err := agent.LoadSession(branchPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot(); len(got) != 4 || got[3].Content != "on the branch" {
		t.Fatalf("reloaded child = %+v", got)
	}
}

func TestForkAtTurnCreatesIndependentSession(t *testing.T) {
	c, sess, path := newSchemaTwoBranchController(t)
	// A guarded turn opens a checkpoint boundary the fork can target.
	c.beginCheckpoint(t.Context(), "second prompt")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "second prompt"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "second answer"})
	if err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	turn := -1
	for candidate := range 8 {
		if c.CheckpointHasBoundary(candidate) {
			turn = candidate
		}
	}
	if turn < 0 {
		t.Fatal("no checkpoint boundary recorded")
	}
	childPath, err := c.ForkNamed(turn, "")
	if err != nil {
		t.Fatalf("ForkNamed: %v", err)
	}
	if c.SessionPath() != childPath || childPath == path || !strings.HasSuffix(childPath, ".jsonl") {
		t.Fatalf("fork must switch to independent child: path %q child %q", c.SessionPath(), childPath)
	}
	if got := len(c.executor.Session().Snapshot()); got != 3 {
		t.Fatalf("forked transcript has %d messages, want the prefix before the turn", got)
	}
	parent, err := agent.LoadSession(path)
	if err != nil {
		t.Fatalf("load parent after fork: %v", err)
	}
	if len(parent.Snapshot()) != 5 {
		t.Fatalf("parent changed after fork: messages=%d", len(parent.Snapshot()))
	}
	if heads, err := agent.ListSessionHeads(path); err != nil || len(heads) != 1 {
		t.Fatalf("new fork added a writable legacy head: %+v err=%v", heads, err)
	}
}

func TestFileBranchesOnlyKeepsFileBranchesForSchemaTwo(t *testing.T) {
	dir := t.TempDir()
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	exec.Session().Add(provider.Message{Role: provider.RoleUser, Content: "root prompt"})
	c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, Label: "test", Sink: event.Discard, FileBranchesOnly: true})
	path := filepath.Join(dir, "root.jsonl")
	c.SetSessionPath(path)
	if err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	branchPath, err := c.Branch("child")
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	if branchPath == path || !strings.HasSuffix(branchPath, ".jsonl") || c.SessionPath() != branchPath {
		t.Fatalf("FileBranchesOnly must keep file branches: returned %q, session path %q", branchPath, c.SessionPath())
	}
	if heads, _ := agent.ListSessionHeads(path); len(heads) != 1 {
		t.Fatalf("file branch must not add heads to the source log: %+v", heads)
	}
}

func TestCommitRewindInPlaceForksRewindHeadAndKeepsController(t *testing.T) {
	c, sess, path := newSchemaTwoBranchController(t)
	c.beginCheckpoint(t.Context(), "second prompt")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "second prompt"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "second answer"})
	if err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	turn := -1
	for candidate := range 8 {
		if c.CheckpointHasBoundary(candidate) {
			turn = candidate
		}
	}
	plan, err := c.PrepareRewind(turn, RewindConversation)
	if err != nil || !plan.CanConversation {
		t.Fatalf("PrepareRewind = %+v err=%v", plan, err)
	}
	result, err := c.CommitRewindInPlace(plan.PlanID)
	if err != nil || !result.OK || !result.ConversationForked || result.Branch == "" || !strings.HasSuffix(result.Branch, ".jsonl") {
		t.Fatalf("CommitRewindInPlace = %+v err=%v", result, err)
	}
	if c.SessionPath() != result.Branch || len(c.executor.Session().Snapshot()) != 3 {
		t.Fatalf("controller after in-place rewind: path %q messages %d", c.SessionPath(), len(c.executor.Session().Snapshot()))
	}
	if heads, err := agent.ListSessionHeads(path); err != nil || len(heads) != 1 {
		t.Fatalf("rewind added a writable legacy head: %+v err=%v", heads, err)
	}
}

func TestBranchTreeMarksTheCurrentHead(t *testing.T) {
	c, _, path := newSchemaTwoBranchController(t)
	if got := c.CurrentBranchID(); got != agent.BranchID(path) {
		t.Fatalf("CurrentBranchID on main = %q, want the file id %q", got, agent.BranchID(path))
	}
	branchPath, err := c.Branch("experiment")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.CurrentBranchID(); got != agent.BranchID(branchPath) {
		t.Fatalf("CurrentBranchID after Branch = %q, want child %q", got, agent.BranchID(branchPath))
	}
	tree := c.BranchTreeText()
	for line := range strings.SplitSeq(tree, "\n") {
		if strings.Contains(line, "experiment") != strings.HasSuffix(line, "current") {
			t.Fatalf("tree marks the wrong branch current:\n%s", tree)
		}
	}
	if _, err := c.SwitchBranch(agent.BranchID(path)); err != nil {
		t.Fatal(err)
	}
	if got := c.CurrentBranchID(); got != agent.BranchID(path) {
		t.Fatalf("CurrentBranchID back on main = %q", got)
	}
}
