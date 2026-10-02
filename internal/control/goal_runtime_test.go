package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/store"
	"reasonix/internal/tool"
)

// goalRuntimeController wires a controller with a scripted reporting model.
// The legacy evaluator option is accepted but must never be called.
func goalRuntimeController(t *testing.T, prov provider.Provider, eval any) (*Controller, *agent.Agent, <-chan event.Event) {
	t.Helper()
	return goalRuntimeControllerWithTokenBudget(t, prov, eval, 0)
}

func goalRuntimeControllerWithTokenBudget(t *testing.T, prov provider.Provider, eval any, tokens int) (*Controller, *agent.Agent, <-chan event.Event) {
	t.Helper()
	ag := agent.New(prov, goalRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
	events := make(chan event.Event, 8)
	c := newOwnedTestController(t, Options{
		Runner:          ag,
		Executor:        ag,
		GoalEvaluator:   eval,
		GoalTokenBudget: tokens,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone || e.Kind == event.Notice {
				events <- e
			}
		}),
	})
	return c, ag, events
}

func TestBudgetClassForBareFaultIsWrite(t *testing.T) {
	// User-reported Chinese bare fault keeps its legacy compatibility class.
	class := budgetClassForLegacyMode("数据模型管理器又出现历史 BUG 了……", GoalResearchAuto)
	if class != budgetClassWrite {
		t.Fatalf("budget class = %q, want write", class)
	}
	// Consultative / diagnostic fault statements stay simple.
	for _, goal := range []string{
		"为什么会出现这个 BUG？",
		"只分析原因，不要修改代码。",
		"诊断数据库连接失败原因。",
		"复现并定位问题，但不要修复。",
	} {
		if got := budgetClassForLegacyMode(goal, GoalResearchAuto); got != budgetClassSimple {
			t.Errorf("budgetClassFor(%q) = %q, want simple", goal, got)
		}
	}
	// Explicit mutation verbs remain write.
	if got := budgetClassForLegacyMode("fix the crash in settings", GoalResearchAuto); got != budgetClassWrite {
		t.Fatalf("explicit fix class = %q, want write", got)
	}
}

func TestGoalLegacyBudgetTokensSidecarAutoResumes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	// Old sidecar: paused solely because of the removed token hard limit.
	state := goalState{
		Goal:             "应用打开设置时崩溃",
		Status:           GoalStatusBlocked,
		StopCause:        stopCauseBudgetTokens,
		Block:            "token budget exhausted (0/200000 tokens used)",
		BudgetClass:      budgetClassWrite,
		TurnsUsed:        1,
		TurnsLimit:       20,
		TokensUsed:       214_000,
		TokensLimit:      200_000,
		BudgetExtensions: 0,
		NoProgressLimit:  0,
		Todos: []evidence.TodoItem{{
			Content: "verify the repaired model mapping", Status: "in_progress",
		}},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.SessionGoalState(path), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	g := &goalMachine{}
	_, _, migrated, _ := g.restoreFromState(path)
	if migrated || !g.disarmed {
		t.Fatal("restore must normalize without writing or activating")
	}
	if g.status != GoalStatusRunning || g.stopCause != "" {
		t.Fatalf("status/stopCause = %q/%q, want running/empty", g.status, g.stopCause)
	}
	if g.block != "" {
		t.Fatalf("block = %q, want empty after legacy token pause migration", g.block)
	}
	if g.tokensUsed != 214_000 {
		t.Fatalf("tokensUsed = %d, want preserved 214000", g.tokensUsed)
	}
	if g.tokensLimit != 0 {
		t.Fatalf("tokensLimit = %d, want 0", g.tokensLimit)
	}
	if g.turnsUsed != 1 || g.turnsLimit != unlimitedGoalTurns {
		t.Fatalf("turns = %d/%d, want 1/unlimited", g.turnsUsed, g.turnsLimit)
	}
	migData, err := os.ReadFile(goalStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	var migratedState goalState
	if err := json.Unmarshal(migData, &migratedState); err != nil {
		t.Fatal(err)
	}
	if len(migratedState.Todos) != 1 || migratedState.Todos[0].Content != "verify the repaired model mapping" {
		t.Fatalf("migration lost persisted todos: %+v", migratedState.Todos)
	}
	// Second load must stay running without re-entering the legacy pause.
	g2 := &goalMachine{}
	if _, _, migrated2, _ := g2.restoreFromState(path); migrated2 {
		t.Fatal("normalized sidecar migrated a second time")
	}
	if g2.status != GoalStatusRunning || g2.stopCause != "" {
		t.Fatalf("second load = %q/%q, want running/empty", g2.status, g2.stopCause)
	}
}

// TestGoalUsageTotalTokensFallback checks the prompt+completion fallback when
// TotalTokens is missing (never double-counting cache hit/miss).
func TestGoalUsageTotalTokensFallback(t *testing.T) {
	u := &provider.Usage{PromptTokens: 100, CompletionTokens: 20, CacheHitTokens: 90}
	if got := usageTotalTokens(u); got != 120 {
		t.Fatalf("fallback = %d, want 120 (prompt+completion, no cache double count)", got)
	}
	u.TotalTokens = 200
	if got := usageTotalTokens(u); got != 200 {
		t.Fatalf("TotalTokens preferred = %d, want 200", got)
	}
}

// TestGoalSidecarCompatRestoresOldAndNewFields pins the compatibility contract:
// an old sidecar without the budget fields restores with re-derived defaults,
// and a new sidecar's pause (blocked + stopCause) survives a controller rebuild
// without failing open.
func TestGoalSidecarCompatRestoresOldAndNewFields(t *testing.T) {
	t.Run("old sidecar restores with defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		// Old sidecar: only goal/status/turns — no budget fields.
		data := []byte(`{"goal":"legacy goal","status":"running","turns":3}`)
		if err := os.WriteFile(store.SessionGoalState(path), data, 0o600); err != nil {
			t.Fatal(err)
		}
		exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
		c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, Label: "test"})
		c.Resume(agent.NewSession("sys"), path)
		rt := c.GoalRuntime()
		if rt.TurnsUsed != 3 {
			t.Fatalf("TurnsUsed = %d, want 3 (legacy Turns carried over)", rt.TurnsUsed)
		}
		if rt.TokensUsed != 0 {
			t.Fatalf("TokensUsed = %d, want 0 (no legacy token record)", rt.TokensUsed)
		}
		if rt.TurnsLimit != 0 || rt.NoProgressLimit != 0 {
			t.Fatalf("removed limits resurfaced: %+v", rt)
		}
		if rt.TokensLimit != 0 {
			t.Fatalf("TokensLimit = %d, want 0 when no budget is configured", rt.TokensLimit)
		}
	})

	t.Run("removed numeric pause auto-migrates on rebuild", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
		c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, SessionPath: path, Label: "test"})
		c.SetGoal("ship the release")
		c.goals.pauseFor(stopCauseBudgetTurns, "turn budget exhausted")
		statePath, data, ok := c.goals.buildStateLocked()
		if !ok {
			t.Fatal("no persisted state")
		}
		if err := os.WriteFile(statePath, data, 0o600); err != nil {
			t.Fatal(err)
		}

		freshExec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
		fresh := newOwnedTestController(t, Options{Executor: freshExec, SessionDir: dir, Label: "fresh"})
		fresh.Resume(agent.NewSession("sys"), path)
		if fresh.GoalStatus() != GoalStatusStopped {
			t.Fatalf("restored status = %q, want running after numeric pause migration", fresh.GoalStatus())
		}
		if rt := fresh.GoalRuntime(); rt.StopCause != "" || rt.TurnsLimit != 0 {
			t.Fatalf("restored runtime = %+v, want continuous Goal", rt)
		}
	})
}

// TestGoalPauseResumeCommands covers the /goal pause and /goal resume CLI
// surface plus the runtime view.
func TestGoalPauseResumeCommands(t *testing.T) {
	cmd, ok := ParseGoalCommand("/goal pause")
	if !ok || cmd.Action != GoalCommandPause {
		t.Fatalf("ParseGoalCommand(/goal pause) = %+v", cmd)
	}
	cmd, ok = ParseGoalCommand("/goal resume")
	if !ok || cmd.Action != GoalCommandResume {
		t.Fatalf("ParseGoalCommand(/goal resume) = %+v", cmd)
	}
	cmd, ok = ParseGoalCommand("/goal")
	if !ok || cmd.Action != GoalCommandStatus {
		t.Fatalf("ParseGoalCommand(/goal) = %+v", cmd)
	}

	c := newOwnedTestController(t, Options{Sink: event.Discard})
	if c.PauseGoal() {
		t.Fatal("PauseGoal without a goal must return false")
	}
	c.SetGoal("long-running research")
	if !c.PauseGoal() {
		t.Fatal("PauseGoal on a running goal must return true")
	}
	if got := c.GoalStatus(); got != GoalStatusBlocked {
		t.Fatalf("GoalStatus() = %q, want blocked", got)
	}
	if rt := c.GoalRuntime(); rt.StopCause != stopCauseManual {
		t.Fatalf("StopCause = %q, want manual", rt.StopCause)
	}
	// The goal text and budget survive the pause.
	if got := c.Goal(); got != "long-running research" {
		t.Fatalf("Goal() = %q, want preserved", got)
	}
	if !c.ResumeGoal() {
		t.Fatal("ResumeGoal on a manually paused goal must return true")
	}
	if got := c.GoalStatus(); got != GoalStatusRunning {
		t.Fatalf("GoalStatus() after resume = %q, want running", got)
	}
	if rt := c.GoalRuntime(); rt.StopCause != "" {
		t.Fatalf("StopCause after resume = %q, want cleared", rt.StopCause)
	}
}

// TestGoalRuntimeViewPopulatesFromController covers the runtime view surface
// the CLI and desktop read.
func TestGoalRuntimeViewPopulatesFromController(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	c.SetGoal("finish the migration")
	rt := c.GoalRuntime()
	if rt.TurnsUsed != 0 || rt.TurnsLimit != 0 || rt.NoProgressLimit != 0 {
		t.Fatalf("runtime view = %+v, want continuous defaults", rt)
	}
	if rt.TokensLimit != 0 {
		t.Fatalf("TokensLimit = %d, want 0 (no hard token limit)", rt.TokensLimit)
	}
}

// minimalFakeTool is a no-op tool for delivery-flow tests.
type minimalFakeTool struct {
	name     string
	readOnly bool
}

func (f minimalFakeTool) Name() string            { return f.name }
func (f minimalFakeTool) Description() string     { return "" }
func (f minimalFakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f minimalFakeTool) ReadOnly() bool          { return f.readOnly }
func (f minimalFakeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return f.name + " done", nil
}

// TestRetiredDeliverySettingDoesNotCreateRecoveryCard covers a historical
// delivery value on an ordinary, non-Goal turn.
func TestRetiredDeliverySettingDoesNotCreateRecoveryCard(t *testing.T) {
	todoWrite, _ := tool.LookupBuiltin("todo_write")
	reg := tool.NewRegistry()
	reg.Add(todoWrite)
	reg.Add(minimalFakeTool{name: "write_file"})
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		{toolCallChunk("w1", "write_file", `{"path":"main.go"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("t0", "todo_write", `{"todos":[{"content":"Ship main","status":"in_progress"}]}`), {Type: provider.ChunkDone}},
		textTurn("premature final"),
		textTurn("must not be consumed by a hidden readiness retry"),
	}}
	// "implement main" is an unanchored mutation. The retired delivery value
	// must not turn its evidence gap into a current pause.
	ag := agent.New(prov, reg, agent.NewSession(""), agent.Options{}, event.Discard)
	done := make(chan event.Event, 1)
	c := newOwnedTestController(t, Options{
		Runner:   ag,
		Executor: ag,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone {
				done <- e
			}
		}),
	})

	if err := c.SetQualityFloor(QualityFloorDelivery); err != nil {
		t.Fatalf("SetQualityFloor: %v", err)
	}
	c.Submit("implement main")
	ev := <-done
	if ev.Readiness != nil {
		t.Fatalf("TurnDone.Readiness = %+v, want no mode-created recovery card", ev.Readiness)
	}
	if prov.call != 3 {
		t.Fatalf("provider calls = %d, want 3 (work + todo + final answer)", prov.call)
	}
	if got := c.GoalStatus(); got != GoalStatusStopped {
		t.Fatalf("GoalStatus() = %q, want stopped (no goal involved)", got)
	}
}
