package goal

import (
	"encoding/json"
	"fmt"
)

type continuationEnvelope struct {
	GoalID        string  `json:"goalId"`
	Revision      uint64  `json:"revision"`
	Objective     string  `json:"objective"`
	Round         uint64  `json:"round"`
	MaxGoalRounds *uint64 `json:"maxGoalRounds"`
}

type recoveryEnvelope struct {
	GoalID        string     `json:"goalId"`
	Revision      uint64     `json:"revision"`
	Objective     string     `json:"objective"`
	Phase         Phase      `json:"phase"`
	Activation    Activation `json:"activation"`
	RoundsStarted uint64     `json:"roundsStarted"`
	StopReason    string     `json:"stopReason,omitempty"`
}

// ContinuationPrompt renders dynamic goal state into a one-shot user message.
// It must never be inserted into the system prompt: keeping the stable prefix
// unchanged preserves provider prompt-cache reuse across autonomous rounds.
func ContinuationPrompt(view View) (string, error) {
	if view.Phase != PhaseActive || view.Activation != ActivationArmed {
		return "", goalError(ErrInvalidTransition, "goal is not eligible for continuation")
	}
	payload, err := json.Marshal(continuationEnvelope{
		GoalID:        view.ID,
		Revision:      view.Revision,
		Objective:     view.Objective,
		Round:         view.RoundsStarted + 1,
		MaxGoalRounds: cloneLimit(view.MaxGoalRounds),
	})
	if err != nil {
		return "", fmt.Errorf("encode goal continuation: %w", err)
	}
	return "Continue making concrete progress on the current goal. Verify the work you perform.\n\n" +
		"<goal-round>\n" + string(payload) + "\n</goal-round>\n\n" +
		"Call get_goal before update_goal and use its exact goal_id and revision. " +
		"Mark complete only when the whole objective is done. If useful work remains, leave the goal active; " +
		"do not treat a round summary as completion. Mark blocked only for a concrete persistent blocker.", nil
}

// RecoveryPrompt renders an existing recoverable goal into the visible user
// turn tail. Paused goals are deliberately excluded: only an explicit host UI
// or command may undo a user pause.
func RecoveryPrompt(view View) (string, error) {
	if view.Phase != PhaseBlocked && !(view.Phase == PhaseActive && view.Activation == ActivationDisarmed) {
		return "", goalError(ErrInvalidTransition, "goal is not eligible for user-authorized recovery")
	}
	payload, err := json.Marshal(recoveryEnvelope{
		GoalID: view.ID, Revision: view.Revision, Objective: view.Objective,
		Phase: view.Phase, Activation: view.Activation,
		RoundsStarted: view.RoundsStarted, StopReason: view.StopReason,
	})
	if err != nil {
		return "", fmt.Errorf("encode goal recovery: %w", err)
	}
	return "An existing long-running goal is waiting for recovery. Interpret the user's request in that context; do not replace the objective with the text of this turn.\n\n" +
		"<goal-recovery>\n" + string(payload) + "\n</goal-recovery>\n\n" +
		"If the user is asking to continue: Call get_goal and then update_goal with action resume using the exact goal_id and revision. " +
		"A user-paused goal may only be resumed through an explicit host UI or command.", nil
}
