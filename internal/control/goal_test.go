package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/store"
	"reasonix/internal/tool"

	_ "reasonix/internal/tool/builtin"
)

// goalRegistry returns a registry carrying the update_goal builtin so scripted
// goal turns can report dispositions through the structured tool.
func goalRegistry() *tool.Registry {
	reg := tool.NewRegistry()
	if t, ok := tool.LookupBuiltin("update_goal"); ok {
		reg.Add(t)
	}
	return reg
}

// goalWireStatus maps FSM status values to the update_goal wire enum.
func goalWireStatus(status string) string {
	switch status {
	case GoalStatusComplete:
		return "complete"
	case GoalStatusBlocked:
		return "blocked"
	default:
		return "continue"
	}
}

// goalToolTurn models one goal turn's provider sequence: the model calls
// update_goal with the given disposition, then answers with text. Call IDs are
// unique per call so recycled scripted turns never collide in the transcript.
func goalToolTurn(status, reason, nextAction string) [][]provider.Chunk {
	args, err := json.Marshal(map[string]string{"status": goalWireStatus(status), "reason": reason, "next_action": nextAction})
	if err != nil {
		panic(err)
	}
	id := fmt.Sprintf("ug-%d", goalToolCallSeq.Add(1))
	return [][]provider.Chunk{
		{toolCallChunk(id, "update_goal", string(args)), {Type: provider.ChunkDone}},
		textTurn("worked on the goal"),
	}
}

var goalToolCallSeq atomic.Uint64

// fakeGoalEvaluator is a scripted bounded Goal evaluator for tests.
type legacyEvaluatorVerdict struct {
	Outcome string
	Reason  string
}

type fakeGoalEvaluator struct {
	outcome string
	reason  string
	err     error
	calls   int
}

func (f *fakeGoalEvaluator) Evaluate(_ context.Context, _ struct{}) (legacyEvaluatorVerdict, error) {
	f.calls++
	if f.err != nil {
		return legacyEvaluatorVerdict{}, f.err
	}
	return legacyEvaluatorVerdict{Outcome: f.outcome, Reason: f.reason}, nil
}

// flattenTurns concatenates per-goal-turn provider sequences into one flat
// scripted provider stream.
func flattenTurns(groups ...[][]provider.Chunk) [][]provider.Chunk {
	var out [][]provider.Chunk
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// toolCallChunk builds a provider turn carrying one tool call.
func toolCallChunk(id, name, args string) provider.Chunk {
	return provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: id, Name: name, Arguments: args}}
}

func TestActiveGoalBlockCarriesTaskContractAndPausePolicy(t *testing.T) {
	block := activeGoalBlock("fix the parser")
	for _, want := range []string{
		"Treat the user's goal as a task contract",
		"Context, Request, Output format, Constraints",
		"Pause policy",
		"irreversible or externally visible operation",
		"the requested scope has changed",
		"information only the user can provide",
		"output format and constraints are satisfied",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("active goal block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "AutoResearch protocol") {
		t.Fatalf("simple goal should not include AutoResearch protocol:\n%s", block)
	}
}

func TestPlainInputWithStrongResearchSignalStaysNormal(t *testing.T) {
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		textTurn("Here is the normal response."),
	}}
	ag := agent.New(prov, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
	events := make(chan event.Event, 8)
	c := newOwnedTestController(t, Options{
		Runner:   ag,
		Executor: ag,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone || e.Kind == event.Notice {
				events <- e
			}
		}),
	})

	c.Submit("持续排查这个线上卡顿直到根因明确，并验证修复")
	waitForTurnDone(t, events)

	if prov.call != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.call)
	}
	first := agent.StripTransientUserBlocks(firstUserMessage(ag.Session().Messages))
	if !strings.HasSuffix(first, "持续排查这个线上卡顿直到根因明确，并验证修复") {
		t.Fatalf("ordinary turn should preserve the original prompt suffix: %q", first)
	}
	if strings.Contains(first, "<active-goal>") || strings.Contains(first, "AutoResearch protocol") {
		t.Fatalf("ordinary prompt should not enter Goal or AutoResearch:\n%s", first)
	}
	if got := c.GoalStatus(); got != GoalStatusStopped {
		t.Fatalf("GoalStatus() = %q, want stopped", got)
	}
}

func TestPlainInputWithStrongResearchSignalPreservesRefsWithoutStartingGoal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("important referenced evidence"), 0o644); err != nil {
		t.Fatal(err)
	}
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		textTurn("Referenced normal response."),
	}}
	ag := agent.New(prov, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
	events := make(chan event.Event, 8)
	c := newOwnedTestController(t, Options{
		WorkspaceRoot: root,
		Runner:        ag,
		Executor:      ag,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone || e.Kind == event.Notice {
				events <- e
			}
		}),
	})

	c.Submit("持续排查直到根因明确，并验证 @notes.txt")
	waitForTurnDone(t, events)

	first := firstUserMessage(ag.Session().Messages)
	for _, want := range []string{
		"important referenced evidence",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("ordinary turn with refs missing %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "<active-goal>") || strings.Contains(first, "AutoResearch protocol") {
		t.Fatalf("ordinary prompt with refs should not enter Goal or AutoResearch:\n%s", first)
	}
	if got := c.GoalStatus(); got != GoalStatusStopped {
		t.Fatalf("GoalStatus() = %q, want stopped", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".reasonix", "autoresearch")); !os.IsNotExist(err) {
		t.Fatalf("ordinary prompt created AutoResearch state: err=%v", err)
	}
}

func TestResearchGoalUsesContinuousRuntimeWithoutArchive(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "s.jsonl")
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionDir: root, Executor: exec})
	c.Resume(sess, sessionPath)
	c.SetGoalWithResearchMode("fix the typo and add a test", GoalResearchOn)
	defer c.Close()
	if got := c.GoalRuntime().TurnsLimit; got != 0 {
		t.Fatalf("research Goal turns limit = %d, want unlimited", got)
	}
	// The class still drives behaviour; it just no longer mints a turn quota.
	if got := c.goals.budgetClass; got != budgetClassResearch {
		t.Fatalf("research Goal budget class = %q, want %q", got, budgetClassResearch)
	}
	if _, err := os.Stat(filepath.Join(root, ".reasonix", "autoresearch")); !os.IsNotExist(err) {
		t.Fatalf("research Goal created legacy archive: %v", err)
	}
	if composed := c.Compose("continue"); strings.Contains(composed, "AutoResearch") || strings.Contains(composed, "autoresearch") {
		t.Fatalf("Goal prompt exposes removed AutoResearch protocol:\n%s", composed)
	}
}

func TestLegacyGoalSidecarMigratesToContinuousRuntimeWithoutTaskID(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLegacyGoalArchive(t, root, "old-task", "archive fallback should not replace sidecar goal")
	if err := os.WriteFile(goalStatePath(sessionPath), []byte(`{"goal":"investigate runtime","status":"running","researchMode":1,"autoResearchTaskID":"old-task"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionDir: root, Executor: exec})
	c.Resume(sess, sessionPath)
	defer c.Close()
	if got := c.GoalRuntime().TurnsLimit; got != 0 {
		t.Fatalf("migrated Goal turns limit = %d, want unlimited", got)
	}
	if got := c.goals.budgetClass; got != budgetClassResearch {
		t.Fatalf("migrated Goal budget class = %q, want %q", got, budgetClassResearch)
	}
	if got := c.Goal(); got != "investigate runtime" {
		t.Fatalf("migrated Goal = %q, want sidecar goal", got)
	}
	raw, err := os.ReadFile(goalStatePath(sessionPath))
	if err != nil {
		t.Fatal(err)
	}
	var state goalState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.AutoResearchTaskID != "old-task" {
		t.Fatalf("read-only restore rewrote the original sidecar: %q", state.AutoResearchTaskID)
	}
}

func TestMissingExplicitLegacyTaskBlocksWithoutCreatingArchive(t *testing.T) {
	root := t.TempDir()
	c := newOwnedTestController(t, Options{WorkspaceRoot: root})
	defer c.Close()
	c.SetGoalWithResearchMode("resume .reasonix/autoresearch/missing-task/", GoalResearchOn)
	if got := c.GoalStatus(); got != GoalStatusBlocked {
		t.Fatalf("GoalStatus = %q, want blocked", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".reasonix", "autoresearch")); !os.IsNotExist(err) {
		t.Fatalf("missing legacy task created archive: %v", err)
	}

	c.SetGoal("resume .reasonix/autoresearch/missing-task/../../escape")
	if got := c.GoalStatus(); got != GoalStatusBlocked {
		t.Fatalf("unsafe legacy path status = %q, want blocked", got)
	}
	if got := c.Goal(); got != "resume .reasonix/autoresearch/missing-task/../../escape" {
		t.Fatalf("unsafe legacy path silently resumed a truncated task: %q", got)
	}
}

func TestExplicitLegacyTaskPathRestoresOriginalGoal(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	taskID := "20260630-original-goal"
	taskRoot := filepath.Join(root, ".reasonix", "autoresearch", taskID)
	if err := os.MkdirAll(filepath.Join(taskRoot, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(taskRoot, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `{"task_id":"` + taskID + `","goal":"find the original root cause","allowed_operations":{"write":true},"success_criteria":[]}`
	progress := `{"status":"running","iteration":2,"updated_at":"2026-06-30T10:00:00Z"}`
	for name, body := range map[string]string{
		"state/task_spec.json":        spec,
		"state/progress.json":         progress,
		"state/directions_tried.json": "[]\n",
		"state/findings.jsonl":        "",
		"state/iteration_log.jsonl":   "",
		"logs/heartbeat.jsonl":        "",
	} {
		if err := os.WriteFile(filepath.Join(taskRoot, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(taskRoot, "state", "task_spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := newOwnedTestController(t, Options{WorkspaceRoot: root})
	defer c.Close()
	c.SetGoalWithResearchMode("resume .reasonix/autoresearch/"+taskID+"/", GoalResearchAuto)
	if got := c.Goal(); got != "find the original root cause" {
		t.Fatalf("Goal() = %q, want original archive goal", got)
	}
	if got := c.GoalRuntime().TurnsLimit; got != 0 {
		t.Fatalf("turns limit = %d, want unlimited", got)
	}
	if got := c.goals.budgetClass; got != budgetClassResearch {
		t.Fatalf("budget class = %q, want %q", got, budgetClassResearch)
	}
	if got := c.GoalStatus(); got != GoalStatusRunning {
		t.Fatalf("status = %q", got)
	}
	after, err := os.ReadFile(filepath.Join(taskRoot, "state", "task_spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("archive task_spec mutated during resume")
	}
}

func TestLegacySidecarEmptyGoalFilledFromArchive(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	sessionPath := filepath.Join(root, "sessions", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	taskID := "fill-from-archive"
	taskRoot := filepath.Join(root, ".reasonix", "autoresearch", taskID)
	if err := os.MkdirAll(filepath.Join(taskRoot, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(taskRoot, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"state/task_spec.json":        `{"task_id":"` + taskID + `","goal":"recover me from archive","allowed_operations":{"write":true},"success_criteria":[]}`,
		"state/progress.json":         `{"status":"running","updated_at":"2026-06-30T10:00:00Z"}`,
		"state/directions_tried.json": "[]\n",
		"state/findings.jsonl":        "",
		"state/iteration_log.jsonl":   "",
		"logs/heartbeat.jsonl":        "",
	} {
		if err := os.WriteFile(filepath.Join(taskRoot, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(goalStatePath(sessionPath), []byte(`{"status":"running","researchMode":1,"autoResearchTaskID":"`+taskID+`","turnsUsed":3,"turnsLimit":40}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionDir: root, Executor: exec})
	c.Resume(sess, sessionPath)
	defer c.Close()
	if got := c.Goal(); got != "recover me from archive" {
		t.Fatalf("Goal() = %q", got)
	}
	if got := c.GoalRuntime().TurnsUsed; got != 3 {
		t.Fatalf("turns used = %d, want preserved 3", got)
	}
	raw, err := os.ReadFile(goalStatePath(sessionPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "autoResearchTaskID") {
		t.Fatalf("sidecar retained task id: %s", raw)
	}
}

func TestPlainInputWithWeakResearchSignalStaysNormal(t *testing.T) {
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		textTurn("Here is a normal answer."),
	}}
	ag := agent.New(prov, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
	events := make(chan event.Event, 4)
	c := newOwnedTestController(t, Options{
		Runner:   ag,
		Executor: ag,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone {
				events <- e
			}
		}),
	})

	c.Submit("长期来看这个模块怎么优化？")
	waitForTurnDone(t, events)

	first := firstUserMessage(ag.Session().Messages)
	if strings.Contains(first, "<active-goal>") || strings.Contains(first, "AutoResearch protocol") {
		t.Fatalf("ordinary prompt should stay outside Goal and AutoResearch:\n%s", first)
	}
	if got := c.GoalStatus(); got != GoalStatusStopped {
		t.Fatalf("GoalStatus() = %q, want stopped", got)
	}
}

func TestCancelStopsIdleGoalWithIncompleteTodos(t *testing.T) {
	ag := agent.New(nil, nil, agent.NewSession(""), agent.Options{}, event.Discard)
	ag.SeedTodoState([]evidence.TodoItem{{Content: "finish the migration", Status: "in_progress"}})
	c := newOwnedTestController(t, Options{Executor: ag, Sink: event.Discard})
	c.SetGoalWithResearchMode("finish the migration", GoalResearchOn)

	c.Cancel()

	if got := c.GoalStatus(); got != GoalStatusStopped {
		t.Fatalf("GoalStatus() = %q, want stopped", got)
	}
	if got := c.Goal(); got != "finish the migration" {
		t.Fatalf("Goal() = %q, want stopped goal text to remain for display/persistence", got)
	}
	if todos := c.Todos(); len(todos) != 0 {
		t.Fatalf("Todos() after stopping idle goal = %+v, want executor seed ignored", todos)
	}
}

// TestSessionRotationClearsActiveGoal pins the /new & /clear goal semantics:
// a fresh session starts with no active goal (so the old goal's text stops
// injecting into its first turns), while the OLD session's persisted
// goal-state sidecar keeps the running goal so resuming it restores the goal.
func TestSessionRotationClearsActiveGoal(t *testing.T) {
	dir := t.TempDir()
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	oldPath := filepath.Join(dir, "session.jsonl")
	c := newOwnedTestController(t, Options{Executor: exec, SystemPrompt: "sys", SessionDir: dir, SessionPath: oldPath, Label: "test"})

	c.SetGoal("ship the release checklist")
	if got := c.Goal(); got != "ship the release checklist" {
		t.Fatalf("Goal() = %q after SetGoal", got)
	}
	if composed := c.Compose("hello"); !strings.Contains(composed, "<active-goal>") {
		t.Fatalf("running goal should inject into turns, composed = %q", composed)
	}

	if err := c.NewSession(); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if got := c.Goal(); got != "" {
		t.Fatalf("Goal() after /new = %q, want empty", got)
	}
	if composed := c.Compose("hello"); strings.Contains(composed, "<active-goal>") {
		t.Fatalf("old goal leaked into the fresh session's turn: %q", composed)
	}
	// The old session keeps its running goal on disk for /resume.
	oldState, err := os.ReadFile(store.SessionGoalState(oldPath))
	if err != nil {
		t.Fatalf("read old goal state: %v", err)
	}
	if !strings.Contains(string(oldState), "ship the release checklist") || !strings.Contains(string(oldState), GoalStatusRunning) {
		t.Fatalf("old session's goal state was disturbed by /new: %s", oldState)
	}
	// The new session's sidecar records the cleared (stopped) state, so
	// profile restores read it as "no running goal".
	newState, err := os.ReadFile(store.SessionGoalState(c.SessionPath()))
	if err != nil {
		t.Fatalf("read new goal state: %v", err)
	}
	if strings.Contains(string(newState), "ship the release checklist") {
		t.Fatalf("new session's goal state carries the old goal: %s", newState)
	}

	// Same contract for /clear.
	c.SetGoal("another goal")
	if err := c.ClearSession(); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	if got := c.Goal(); got != "" {
		t.Fatalf("Goal() after /clear = %q, want empty", got)
	}
	if composed := c.Compose("hello"); strings.Contains(composed, "<active-goal>") {
		t.Fatalf("old goal leaked into the cleared session's turn: %q", composed)
	}
}

func TestGoalSidecarRoundTripPreservesBlockedDeliveryCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, SessionPath: path, Label: "test"})
	c.SetGoal("finish the delivery")
	scopeID, _, ok := c.goals.deliveryScope()
	if !ok || scopeID == "" {
		t.Fatal("Goal did not allocate a delivery scope")
	}
	cp := evidence.DeliveryCheckpoint{
		ScopeID:             scopeID,
		CriteriaEstablished: true,
		WorkObserved:        true,
		MutationObserved:    true,
		PendingMutation:     true,
	}
	statePath, data, persist := c.goals.setDeliveryCheckpoint(cp)
	c.persistGoalState(statePath, data, persist)
	c.stopGoal(GoalStatusBlocked)

	freshExec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	fresh := newOwnedTestController(t, Options{Executor: freshExec, SessionDir: dir, Label: "fresh"})
	fresh.Resume(agent.NewSession("sys"), path)
	if fresh.Goal() != "finish the delivery" || fresh.GoalStatus() != GoalStatusBlocked {
		t.Fatalf("restored Goal = (%q, %q), want blocked Goal", fresh.Goal(), fresh.GoalStatus())
	}
	if got := freshExec.DeliveryCheckpoint(); got != cp {
		t.Fatalf("restored checkpoint = %+v, want %+v", got, cp)
	}
	if !fresh.ResumeGoal() {
		t.Fatal("ResumeGoal rejected a restored blocked Goal")
	}
	id, _, ok := fresh.goals.deliveryScope()
	if !ok || id != scopeID {
		t.Fatalf("resumed scope = %q, want %q", id, scopeID)
	}
}

func TestLegacyRunningGoalSidecarAllocatesScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	data := []byte(`{"goal":"legacy goal","status":"running"}`)
	if err := os.WriteFile(store.SessionGoalState(path), data, 0o600); err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, Label: "test"})
	c.Resume(agent.NewSession("sys"), path)
	if c.goals.active() {
		t.Fatal("restored goal automatically active")
	}
	if !c.ResumeGoal() {
		t.Fatal("explicit resume failed")
	}
	id, task, ok := c.goals.deliveryScope()
	if !ok || id == "" || task != "legacy goal" {
		t.Fatalf("legacy delivery scope = (%q, %q, %v)", id, task, ok)
	}
	if got := exec.DeliveryCheckpoint(); got.ScopeID != id {
		t.Fatalf("legacy checkpoint scope = %q, want %q", got.ScopeID, id)
	}
}
