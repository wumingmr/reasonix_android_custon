package control

import (
	"context"
	"errors"
	"strings"

	goaldomain "reasonix/internal/goal"
)

// EditGoalDurable edits the current v3 Goal without replacing its identity or
// resetting admitted rounds. A nil limit explicitly selects unlimited rounds.
func (c *Controller) EditGoalDurable(objective string, maxGoalRounds *uint64) error {
	if !c.sessionEngineEnabled() {
		return errors.New("editing a goal in place requires a linear v3 session")
	}
	current, err := c.goalLifecycleView()
	if err != nil {
		return err
	}
	if current == nil {
		return errors.New("no goal is available to edit")
	}
	objective = strings.TrimSpace(objective)
	_, err = c.applyHostGoalMutation(context.Background(), "edit", func(machine *goaldomain.Machine) (*goaldomain.View, error) {
		edited, editErr := machine.Edit(current.Ref(), goaldomain.EditRequest{
			Objective:     &objective,
			MaxGoalRounds: goaldomain.RoundLimitChange{Set: true, Value: maxGoalRounds},
		})
		return &edited, editErr
	})
	if err == nil && current.Phase == goaldomain.PhaseActive && current.Activation == goaldomain.ActivationArmed {
		c.kickGoalDriver()
	}
	return err
}
