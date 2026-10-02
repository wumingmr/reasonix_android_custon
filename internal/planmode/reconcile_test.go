package planmode_test

import (
	"testing"

	"reasonix/internal/planmode"
	"reasonix/internal/tool"

	_ "reasonix/internal/tool/builtin"
)

func TestBuiltinPhaseClassifiersMatchPolicy(t *testing.T) {
	builtins := tool.Builtins()
	if len(builtins) == 0 {
		t.Fatal("tool.Builtins() is empty")
	}
	for _, tl := range builtins {
		safety := planmode.PlanSafetyUnknown
		if classifier, ok := tl.(tool.PlanModeClassifier); ok {
			if classifier.PlanModeSafe() {
				safety = planmode.PlanSafetySafe
			} else {
				safety = planmode.PlanSafetyUnsafe
			}
		}
		got := (planmode.Policy{}).Decide(planmode.Call{
			Name:     tl.Name(),
			ReadOnly: tl.ReadOnly(),
			Safety:   safety,
		})
		if got.Blocked != (safety == planmode.PlanSafetyUnsafe) {
			t.Errorf("builtin %q safety=%v decision=%+v", tl.Name(), safety, got)
		}
	}
}

func TestRetiredCompleteStepIsNotDiscoverable(t *testing.T) {
	if _, ok := tool.LookupBuiltin("complete_step"); ok {
		t.Fatal("retired complete_step builtin remains discoverable")
	}
}
