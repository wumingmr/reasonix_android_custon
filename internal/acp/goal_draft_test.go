package acp

import "testing"

func TestLoadedGoalDraftModeRequiresMissingGoal(t *testing.T) {
	if loadedGoalDraftMode("finish the restored target") {
		t.Fatal("an existing stopped/disarmed goal was treated as a new-goal draft")
	}
	if !loadedGoalDraftMode("  ") {
		t.Fatal("a session without a goal must keep draft mode for the next prompt")
	}
}

func TestSelectingGoalModeDoesNotReplaceExistingLifecycle(t *testing.T) {
	for _, objective := range []string{"restored active goal", "blocked goal", "paused goal", "completed goal"} {
		if selectedGoalDraftMode(sessionModeGoal, objective) {
			t.Fatalf("Goal mode treated existing objective %q as a draft", objective)
		}
	}
	if !selectedGoalDraftMode(sessionModeGoal, "") {
		t.Fatal("Goal mode without a lifecycle did not arm a new-goal draft")
	}
	if selectedGoalDraftMode(sessionModePlan, "") {
		t.Fatal("Plan mode armed a Goal draft")
	}
}
