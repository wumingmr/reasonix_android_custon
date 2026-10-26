package agent

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLiveCompactRatioUsesOneSnapshotPerDecision(t *testing.T) {
	var bits atomic.Uint64
	bits.Store(math.Float64bits(.85))
	reads := 0
	a := &Agent{agentConfig: agentConfig{contextWindow: 100_000, compactRatio: .85, compactRatioSource: func() float64 { reads++; return math.Float64frombits(bits.Load()) }}}
	if got := a.compactTrigger(); got != 85_000 || reads != 1 {
		t.Fatalf("trigger=%d reads=%d", got, reads)
	}
	bits.Store(math.Float64bits(.80))
	if got := a.compactTrigger(); got != 80_000 || reads != 2 {
		t.Fatalf("updated trigger=%d reads=%d", got, reads)
	}
	if a.CompactRatio() != .80 {
		t.Fatal("status threshold disagrees")
	}
	bits.Store(math.Float64bits(0))
	if a.CompactRatio() != defaultCompactRatio || a.compactTrigger() != 80_000 {
		t.Fatal("unset live ratio lost default behavior")
	}
	fixed := &Agent{agentConfig: agentConfig{compactRatio: .7}}
	if fixed.CompactRatio() != .7 {
		t.Fatal("standalone construction value changed")
	}
}

func TestTaskCompactRatioSnapshotsNewChildren(t *testing.T) {
	var bits atomic.Uint64
	bits.Store(math.Float64bits(.85))
	source := func() float64 { return math.Float64frombits(bits.Load()) }
	task := NewTaskToolWithOptions(TaskToolOptions{CompactRatio: .85, CompactRatioSource: source})
	before := task.subagentOptions(t.Context(), 0, nil, 100_000, 1, "", nil)
	bits.Store(math.Float64bits(.8))
	after := task.subagentOptions(t.Context(), 0, nil, 100_000, 1, "", nil)
	if before.CompactRatio != .85 || after.CompactRatio != .8 || before.CompactRatioSource != nil || after.CompactRatioSource != nil {
		t.Fatal("children did not freeze their creation-time threshold")
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			bits.Store(math.Float64bits(.7))
			bits.Store(math.Float64bits(.8))
		}
	})
	for range 1000 {
		opts := task.subagentOptions(t.Context(), 0, nil, 100_000, 1, "", nil)
		if opts.CompactRatio != .7 && opts.CompactRatio != .8 {
			t.Fatalf("torn child threshold %v", opts.CompactRatio)
		}
	}
	wg.Wait()
}
