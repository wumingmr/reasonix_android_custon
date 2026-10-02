package main

import (
	"context"

	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

func desktopLegacyImportOptions(workspaceRoot string) session.CreateOptions {
	cwd := canonicalRuntimeRoot(workspaceRoot)
	if cwd == "" {
		cwd = globalWorkspaceRoot()
	}
	return session.CreateOptions{
		CWD: cwd, Origin: session.SessionOriginLegacyImport,
	}
}

// sessionBinding is intentionally smaller than the public desktop control
// surface. It lets rebuild code preserve the exact host-owned Runtime without
// making legacy test controllers implement the final identity API.
type persistedSessionBinding interface {
	SessionBinding() (*session.Service, *session.Runtime, bool)
}

func exclusiveSessionBinding(ctrl control.SessionAPI) (*session.Service, *session.Runtime, bool) {
	bound, ok := ctrl.(persistedSessionBinding)
	if !ok || bound == nil {
		return nil, nil, false
	}
	service, runtime, exclusive := bound.SessionBinding()
	return service, runtime, exclusive && service != nil && runtime != nil
}

// buildDesktopControllerReplacement uses boot.Rebuild for an already-bound
// final-format session. That path keeps the SessionRuntime and its writer while
// replacing only the Agent/configuration graph. Legacy controllers retain the
// old build-and-resume path until they cross the explicit migration boundary.
func buildDesktopControllerReplacement(ctx context.Context, old control.SessionAPI, opts boot.Options) (control.SessionAPI, bool, error) {
	concrete, ok := old.(*control.Controller)
	if !ok || concrete == nil {
		ctrl, err := boot.Build(ctx, opts)
		return ctrl, false, err
	}
	if opts.SessionService == nil {
		ctrl, err := boot.Build(ctx, opts)
		return ctrl, false, err
	}
	if opts.SessionTemp == nil {
		opts.SessionTemp = concrete.SessionTemp()
	}
	if _, _, bound := exclusiveSessionBinding(old); !bound {
		opts.SessionCreateOptions = desktopLegacyImportOptions(opts.WorkspaceRoot)
	}
	result, err := boot.Rebuild(ctx, concrete, opts)
	if err != nil {
		return nil, true, err
	}
	return result.Controller, true, nil
}

// retireReplacedController must not close a SessionRuntime still used by its
// replacement. ReleaseResources tears down only the retired Agent generation.
func retireReplacedController(old, replacement control.SessionAPI) {
	if old == nil || old == replacement {
		return
	}
	_, oldRuntime, oldExclusive := exclusiveSessionBinding(old)
	_, newRuntime, newExclusive := exclusiveSessionBinding(replacement)
	if oldExclusive && newExclusive && oldRuntime == newRuntime {
		if concrete, ok := replacement.(*control.Controller); ok {
			concrete.ActivateGoalDriverAfterRebuild()
		}
		if concrete, ok := old.(*control.Controller); ok {
			concrete.ReleaseResources()
			return
		}
	}
	old.Close()
}

// activateReplacementController is called only inside the host's final
// compare-and-publish critical section. It transfers an exclusive Session
// Runtime without letting construction-time candidates steal Stop or commit
// authority from the controller still visible in the tab.
func activateReplacementController(old, replacement control.SessionAPI) error {
	return control.ActivateSessionAPIReplacement(old, replacement)
}

func discardReplacementController(candidate, current control.SessionAPI) {
	if candidate == nil || candidate == current {
		return
	}
	_, candidateRuntime, candidateExclusive := exclusiveSessionBinding(candidate)
	_, currentRuntime, currentExclusive := exclusiveSessionBinding(current)
	if candidateExclusive && currentExclusive && candidateRuntime == currentRuntime {
		if concrete, ok := candidate.(*control.Controller); ok {
			concrete.ReleaseResources()
			return
		}
	}
	candidate.Close()
}
