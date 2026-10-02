package acp

import "testing"

func TestModelDeltaClearsInheritedEffortAndKeepsExplicitNewSelection(t *testing.T) {
	high, low := "high", "low"
	p := SessionConfigStateParams{Model: "one", EffortOverride: &high}
	(sessionConfigDelta{axis: "model", model: "one"}).applyTo(&p)
	if p.EffortOverride == nil || *p.EffortOverride != high {
		t.Fatal("same model lost intent")
	}
	(sessionConfigDelta{axis: "model", model: "two"}).applyTo(&p)
	if p.EffortOverride != nil {
		t.Fatal("different model inherited effort")
	}
	(sessionConfigDelta{axis: "thought_level", effortOverride: &low}).applyTo(&p)
	if p.EffortOverride == nil || *p.EffortOverride != low {
		t.Fatal("new model's explicit effort lost")
	}
}

func TestQueuedModelReplacementDoesNotReviveOlderEffort(t *testing.T) {
	high := "high"
	for _, target := range []string{"one", "two"} {
		queue := mergePendingConfig(nil, sessionConfigDelta{axis: "model", model: "one"})
		queue = mergePendingConfig(queue, sessionConfigDelta{axis: "thought_level", effortOverride: &high})
		queue = mergePendingConfig(queue, sessionConfigDelta{axis: "model", model: target})
		params := SessionConfigStateParams{Model: "initial"}
		for _, delta := range queue {
			delta.applyTo(&params)
		}
		if params.Model != target || (params.EffortOverride != nil) != (target == "one") {
			t.Fatalf("target %s: %+v", target, params)
		}
	}
}
