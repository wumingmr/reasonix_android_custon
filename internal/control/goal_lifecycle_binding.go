package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
)

type legacyGoalProjection struct {
	Goal      string `json:"goal"`
	Status    string `json:"status"`
	TurnsUsed int    `json:"turnsUsed"`
	Block     string `json:"block"`
}

// goalLifecycleFromProjection is the only compatibility boundary between
// imported legacy goal sidecars and the current versioned goal domain. It does
// not write during cold open and never restores todo or process activation.
func goalLifecycleFromProjection(raw json.RawMessage, sessionID string, createdAt time.Time) (*goaldomain.Machine, error) {
	machine := goaldomain.NewMachine(nil, nil)
	if len(raw) == 0 {
		return machine, nil
	}
	var header struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return machine, fmt.Errorf("decode goal projection: %w", err)
	}
	if header.Version != nil {
		if _, err := machine.Restore(raw); err != nil {
			return machine, err
		}
		return machine, nil
	}
	return importLegacyGoalProjection(machine, raw, sessionID, createdAt)
}

func importLegacyGoalProjection(machine *goaldomain.Machine, raw json.RawMessage, sessionID string, createdAt time.Time) (*goaldomain.Machine, error) {
	var legacy legacyGoalProjection
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return machine, fmt.Errorf("decode legacy goal projection: %w", err)
	}
	legacy.Goal = strings.TrimSpace(legacy.Goal)
	if legacy.Goal == "" {
		return machine, nil
	}
	if legacy.TurnsUsed < 0 {
		return machine, fmt.Errorf("legacy goal has negative admitted rounds")
	}
	var phase goaldomain.Phase
	var blockedReason *goaldomain.BlockReason
	switch strings.TrimSpace(legacy.Status) {
	case GoalStatusRunning:
		phase = goaldomain.PhaseActive
	case GoalStatusComplete:
		phase = goaldomain.PhaseComplete
	case GoalStatusBlocked:
		phase = goaldomain.PhaseBlocked
		message := strings.TrimSpace(legacy.Block)
		if message == "" {
			message = "legacy goal stopped without a recorded reason"
		}
		blockedReason = &goaldomain.BlockReason{Code: "legacy-blocked", Message: message}
	case "", GoalStatusStopped:
		phase = goaldomain.PhasePaused
	default:
		return machine, fmt.Errorf("legacy goal has unsupported status %q", legacy.Status)
	}
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	} else {
		createdAt = createdAt.UTC()
	}
	hash := sha256.Sum256(append(append([]byte(sessionID), 0), raw...))
	id := "legacy-" + hex.EncodeToString(hash[:12])
	document := map[string]any{
		"version": goaldomain.StateVersion,
		"current": goaldomain.Snapshot{
			ID: id, Revision: 1, Objective: legacy.Goal, Phase: phase,
			MaxGoalRounds: nil, RoundsStarted: uint64(legacy.TurnsUsed),
			BlockedReason: blockedReason, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		"legacyState": json.RawMessage(append([]byte(nil), raw...)),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return machine, err
	}
	if _, err := machine.Restore(encoded); err != nil {
		return machine, err
	}
	return machine, nil
}

func (c *Controller) installGoalLifecycle(runtime *session.Runtime) {
	machine := goaldomain.NewMachine(nil, nil)
	var loadErr error
	if runtime != nil && runtime.Session() != nil {
		snapshot := runtime.Session().ExecutionSnapshot()
		createdAt := runtime.Session().Handle().Manifest().CreatedAt
		machine, loadErr = goalLifecycleFromProjection(snapshot.Projection.GoalState, runtime.Ref().SessionID, createdAt)
	}
	c.goalLifecycleMu.Lock()
	c.goalLifecycle = machine
	c.goalLifecycleLoadErr = loadErr
	c.goalLifecycleMu.Unlock()
}

func (c *Controller) goalLifecycleView() (*goaldomain.View, error) {
	if c == nil {
		return nil, nil
	}
	c.goalLifecycleMu.RLock()
	machine, loadErr := c.goalLifecycle, c.goalLifecycleLoadErr
	c.goalLifecycleMu.RUnlock()
	if loadErr != nil {
		return nil, loadErr
	}
	if machine == nil {
		return nil, nil
	}
	return machine.Get(), nil
}
