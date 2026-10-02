package main

import (
	"fmt"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/provider"
)

func (a *App) buildLegacySettingReplacement(
	tab *WorkspaceTab,
	runtime normalizedTabRuntime,
	opts boot.Options,
	oldCtrl control.SessionAPI,
	carried []provider.Message,
	prevPath, setting string,
) (control.SessionAPI, normalizedTabRuntime, string, error) {
	if old, ok := oldCtrl.(*control.Controller); ok && old != nil && opts.SessionTemp == nil {
		opts.SessionTemp = old.SessionTemp()
	}
	restoreLegacyEvents := func() {}
	if old, ok := oldCtrl.(*control.Controller); ok && old != nil {
		var err error
		restoreLegacyEvents, err = old.SuspendLegacyEventStoreForImport(a.bootContext())
		if err != nil {
			return nil, normalizedTabRuntime{}, "", fmt.Errorf("freeze settings rebuild source: %w", err)
		}
	}
	published := false
	defer func() {
		if !published {
			restoreLegacyEvents()
		}
	}()
	ctrl, err := boot.Build(a.bootContext(), opts)
	if err != nil {
		return nil, normalizedTabRuntime{}, "", err
	}
	a.bindControllerDisplayRecorder(ctrl)
	configureControllerRuntime(ctrl, oldCtrl, runtime)
	path := agent.ContinueSessionPath(prevPath, ctrl.SessionDir(), ctrl.Label())
	if err := a.ensureTabSessionLeaseForRebuild(tab, path, setting); err != nil {
		ctrl.Close()
		return nil, normalizedTabRuntime{}, "", err
	}
	restored, err := resumeControllerRuntimeWithMessages(ctrl, carried, path, runtime)
	if err != nil {
		ctrl.Close()
		return nil, normalizedTabRuntime{}, "", err
	}
	published = true
	return ctrl, restored, path, nil
}
