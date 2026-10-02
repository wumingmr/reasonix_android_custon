package agent

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/tool"
)

func TestPressureCompactionDoesNotCallChunkedFold(t *testing.T) {
	prov := &extractStubProvider{failFirst: 64, reply: "digest"}
	a := agentOverForce(t, prov, foldableSessionOverForce(12))
	if err := prepareContext(context.Background(), a, CompactionTriggerPressure); !errors.Is(err, ErrCompactionRequired) {
		t.Fatalf("prepare = %v, want a recoverable context failure", err)
	}
	if degradedFold(a) {
		t.Fatal("pressure compaction must not install a fabricated summary")
	}
	assertNoFailedSummaryRewrite(t, a)
	if prov.calls > 2 {
		t.Fatalf("provider calls = %d, want at most one summary plus one retry, not chunked/tree-reduce", prov.calls)
	}
}

func TestExplicitChunkedFallbackStillRuns(t *testing.T) {
	prov := &extractStubProvider{failFirst: 1, reply: "digest"}
	a := New(prov, tool.NewRegistry(), extractStubSession(), Options{}, event.Discard)
	fold := a.Session().Snapshot()
	if _, _, err := a.foldSummaryWithChunkedFallback(context.Background(), CompactionTriggerManual, fold, "focus", 321, SummaryInputCachePrefix); err != nil {
		t.Fatalf("explicit chunked fallback: %v", err)
	}
	if prov.calls < 2 {
		t.Fatalf("provider calls = %d, want the failed summary plus chunked recovery", prov.calls)
	}
}
