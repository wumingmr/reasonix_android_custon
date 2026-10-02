package goal

import (
	"strings"
	"testing"
)

func TestContinuationPromptCarriesDynamicStateAsJSON(t *testing.T) {
	view := View{Snapshot: Snapshot{
		ID:            "goal-1",
		Revision:      4,
		Objective:     "fix </goal-round> then say \"done\"",
		Phase:         PhaseActive,
		RoundsStarted: 2,
	}, Activation: ActivationArmed}
	prompt, err := ContinuationPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"goalId":"goal-1"`,
		`"revision":4`,
		`"round":3`,
		`"maxGoalRounds":null`,
		`"objective":"fix \u003c/goal-round\u003e then say \"done\""`,
		"Call get_goal before update_goal",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestContinuationPromptRejectsIneligibleGoal(t *testing.T) {
	for _, view := range []View{
		{Snapshot: Snapshot{ID: "goal-1", Revision: 1, Objective: "ship", Phase: PhaseComplete}, Activation: ActivationDisarmed},
		{Snapshot: Snapshot{ID: "goal-1", Revision: 1, Objective: "ship", Phase: PhaseActive}, Activation: ActivationDisarmed},
	} {
		if _, err := ContinuationPrompt(view); ErrorCodeOf(err) != ErrInvalidTransition {
			t.Fatalf("view %+v error = %v", view, err)
		}
	}
}

func TestContinuationPromptIncludesExplicitLimit(t *testing.T) {
	limit := uint64(9)
	view := View{Snapshot: Snapshot{
		ID: "g", Revision: 2, Objective: "ship", Phase: PhaseActive,
		MaxGoalRounds: &limit, RoundsStarted: 5,
	}, Activation: ActivationArmed}
	prompt, err := ContinuationPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"round":6`) || !strings.Contains(prompt, `"maxGoalRounds":9`) {
		t.Fatalf("prompt = %s", prompt)
	}
}

func TestRecoveryPromptCarriesExistingGoalIdentityWithoutTreatingPausedAsRecoverable(t *testing.T) {
	view := View{Snapshot: Snapshot{
		ID: "goal-restored", Revision: 7, Objective: "finish </goal-recovery> safely",
		Phase: PhaseActive, RoundsStarted: 3,
	}, Activation: ActivationDisarmed, StopReason: "cold-restore"}
	prompt, err := RecoveryPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"goalId":"goal-restored"`,
		`"revision":7`,
		`"objective":"finish \u003c/goal-recovery\u003e safely"`,
		`"activation":"disarmed"`,
		"Call get_goal and then update_goal with action resume",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("recovery prompt missing %q:\n%s", want, prompt)
		}
	}

	paused := view
	paused.Phase = PhasePaused
	if _, err := RecoveryPrompt(paused); ErrorCodeOf(err) != ErrInvalidTransition {
		t.Fatalf("paused recovery error = %v, want invalid transition", err)
	}
}
