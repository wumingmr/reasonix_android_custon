package goal

import (
	"math"
	"strings"
	"testing"
)

func TestWorkDurationPersistsWithoutChangingAuthority(t *testing.T) {
	machine := newTestMachine(t)
	created, err := machine.Create(CreateRequest{Objective: "finish"})
	if err != nil {
		t.Fatal(err)
	}
	measured, err := machine.AddWorkDuration(created.Ref(), 1234)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Ref() != created.Ref() || measured.WorkDurationMs != 1234 {
		t.Fatalf("measured = %+v", measured)
	}
	cloned := machine.Clone()
	if _, err := cloned.AddWorkDuration(created.Ref(), 10); err != nil {
		t.Fatal(err)
	}
	if machine.Get().WorkDurationMs != 1234 {
		t.Fatal("candidate mutated published goal")
	}
	data, err := cloned.Encode()
	if err != nil {
		t.Fatal(err)
	}
	restored := newTestMachine(t)
	view, err := restored.Restore(data)
	if err != nil {
		t.Fatal(err)
	}
	if view.WorkDurationMs != 1244 || view.Activation != ActivationDisarmed {
		t.Fatalf("restored = %+v", view)
	}
	invalid := strings.Replace(string(data), `"workDurationMs":1244`, `"workDurationMs":-1`, 1)
	if _, err := restored.Restore([]byte(invalid)); err == nil {
		t.Fatal("negative persisted work accepted")
	}
}

func TestWorkDurationRejectsStaleRefAndSaturates(t *testing.T) {
	machine := newTestMachine(t)
	created, err := machine.Create(CreateRequest{Objective: "finish"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machine.AddWorkDuration(Ref{ID: "old", Revision: created.Revision}, 10); ErrorCodeOf(err) != ErrStaleRevision {
		t.Fatalf("stale error = %v", err)
	}
	for _, duration := range []int64{0, -1, math.MaxInt64, 10} {
		if _, err := machine.AddWorkDuration(created.Ref(), duration); err != nil {
			t.Fatal(err)
		}
	}
	if got := machine.Get().WorkDurationMs; got != math.MaxInt64 {
		t.Fatalf("duration overflow = %d", got)
	}
}

func TestWorkDurationDoesNotChangeGoalPrompts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation Activation
		prompt     func(View) (string, error)
	}{
		{"continuation", ActivationArmed, ContinuationPrompt}, {"recovery", ActivationDisarmed, RecoveryPrompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := View{Snapshot: Snapshot{ID: "goal", Revision: 1, Objective: "ship", Phase: PhaseActive}, Activation: tc.activation}
			before, err := tc.prompt(view)
			if err != nil {
				t.Fatal(err)
			}
			view.WorkDurationMs = 123456
			after, err := tc.prompt(view)
			if err != nil {
				t.Fatal(err)
			}
			if before != after || strings.Contains(after, "workDurationMs") {
				t.Fatalf("duration changed %s prompt", tc.name)
			}
		})
	}
}
