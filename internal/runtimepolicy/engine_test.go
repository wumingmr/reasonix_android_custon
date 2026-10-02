package runtimepolicy

import (
	"encoding/json"
	"sync"
	"testing"

	"reasonix/internal/evidence"
)

func TestMergeDecisionsIsMonotonic(t *testing.T) {
	allow := GuardDecision{Action: GuardAllow}
	ask := GuardDecision{Action: GuardAsk, Message: "ask"}
	deny := GuardDecision{Action: GuardDeny, Message: "deny"}
	got := MergeDecisions(allow, deny, ask)
	if got.Action != GuardDeny || got.Message != "deny" {
		t.Fatalf("merge = %+v", got)
	}
	got = MergeDecisions(ask, allow)
	if got.Action != GuardAsk {
		t.Fatalf("ask must outrank allow: %+v", got)
	}
}

func TestPlanGuardBeatsYOLO(t *testing.T) {
	ctx := CallContext{
		PlanReadOnly: true,
		Profile:      evidence.EffectProfile{Known: true, WorkspaceWrite: true},
	}
	if (PlanGuard{}).BeforeTool(ctx).Action != GuardDeny {
		t.Fatal("plan writes must deny even when permission would allow")
	}
}

func TestOpaqueWriterAskOrDeny(t *testing.T) {
	ctx := CallContext{Profile: evidence.EffectProfile{WorkspaceWrite: true, Reason: evidence.ReasonOpaqueWriter}}
	if (OpaqueWriterGuard{}).BeforeTool(ctx).Action != GuardDeny {
		t.Fatal("headless unknown writer must deny")
	}
	ctx.Interactive = true
	if (OpaqueWriterGuard{}).BeforeTool(ctx).Action != GuardAsk {
		t.Fatal("interactive unknown writer must ask")
	}
}

func TestConstraintNoWrite(t *testing.T) {
	g := ConstraintGuard{Constraints: Constraints{ForbidMutation: true}}
	ctx := CallContext{Profile: evidence.EffectProfile{Known: true, WorkspaceWrite: true}}
	if g.BeforeTool(ctx).Action != GuardDeny {
		t.Fatal("explicit no-write must deny")
	}
}

func TestConcurrentReadOnlyBeforeTool(t *testing.T) {
	e := NewEngine(Constraints{})
	ctx := CallContext{Profile: evidence.EffectProfile{Known: true, ReadOnly: true}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			<-start
			if e.BeforeTool(ctx).Action == GuardDeny {
				t.Error("read-only overlap must not deny")
			}
		}()
	}
	close(start)
	wg.Wait()
}

func TestFailedWriterBarrier(t *testing.T) {
	g := MutationDependencyGuard{Blocked: true}
	ctx := CallContext{Profile: evidence.EffectProfile{Known: true, WorkspaceWrite: true}}
	if g.BeforeTool(ctx).Action != GuardDeny {
		t.Fatal("failed writer must block later mutations")
	}
	read := CallContext{Profile: evidence.EffectProfile{Known: true, ReadOnly: true}}
	if g.BeforeTool(read).Action != GuardAbstain {
		t.Fatal("read-only diagnosis may still run after a failed writer")
	}
}

// Risk, file count, and verification requests do not become prerequisites.
func TestWritesNeedNoQualityContract(t *testing.T) {
	for _, prompt := range []string{"fix the code", "完整验证并交付", "run all tests"} {
		e := NewEngine(ParseConstraints(prompt))
		var wg sync.WaitGroup
		for _, path := range []string{"README.md", "internal/auth/session.go", "schema/migration.sql", "internal/agent/agent.go"} {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				if got := e.BeforeTool(writeCall(path)); got.Action != GuardAbstain {
					t.Errorf("%s: unexpected quality precondition: %+v", path, got)
				}
			}(path)
		}
		wg.Wait()
	}
}

func writeCall(path string) CallContext {
	args := json.RawMessage(`{"path":"` + path + `"}`)
	return CallContext{
		ToolName: "edit_file",
		Args:     args,
		Profile: evidence.ClassifyEffect(evidence.EffectInput{
			ToolName: "edit_file", Args: args, ActualPaths: []string{path},
		}),
	}
}
