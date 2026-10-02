package control

import (
	"fmt"
	"reasonix/internal/jobs"
)

// ReserveBackgroundReplacement supports hosts with their own candidate factory.
// The caller holds its session admission lock through publish or disposal.
func ReserveBackgroundReplacement(previous any) (*jobs.SessionBackgroundScope, func(*Controller) error, func(), error) {
	old, ok := previous.(*Controller)
	if !ok || old.background.scope == nil {
		return nil, func(*Controller) error { return nil }, func() {}, nil
	}
	if ModelReplacementBlocked(old) {
		return nil, nil, nil, fmt.Errorf("runtime-dependent work prevents configuration replacement")
	}
	scope := old.background.scope
	release, err := scope.Manager.BeginReplacement("")
	if err != nil {
		return nil, nil, nil, err
	}
	transferred := false
	finish := func(candidate *Controller) error {
		if candidate == nil || candidate.background.scope != scope {
			return fmt.Errorf("session factory did not preserve background ownership")
		}
		candidate.StageBackgroundReplacement(release)
		transferred = true
		return nil
	}
	return scope, finish, func() {
		if !transferred {
			release()
		}
	}, nil
}
