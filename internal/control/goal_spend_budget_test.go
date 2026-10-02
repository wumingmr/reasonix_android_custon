package control

import (
	"testing"

	"reasonix/internal/provider"
)

func billedGoalTurn() []provider.Chunk {
	return []provider.Chunk{
		{Type: provider.ChunkText, Text: "still working."},
		{Type: provider.ChunkUsage, Usage: &provider.Usage{
			PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, RequestCount: 1}},
		{Type: provider.ChunkDone},
	}
}

// The class-derived turn quota is gone: a Goal is no longer bounded by a number
// guessed from its own text. What bounds it is what the user configures.
func TestGoalStartsWithNoTurnQuota(t *testing.T) {
	prov := &scriptedTurns{turns: [][]provider.Chunk{billedGoalTurn()}}
	c, _, _ := goalRuntimeController(t, prov, &fakeGoalEvaluator{outcome: "complete", reason: "done"})
	c.SetGoal("research the whole subsystem end to end")

	if rt := c.GoalRuntime(); rt.TurnsLimit != 0 || rt.TokensLimit != 0 {
		t.Fatalf("runtime = %+v, want an unbounded goal out of the box", rt)
	}
}
