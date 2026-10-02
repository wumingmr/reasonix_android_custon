package main

import (
	"fmt"

	"reasonix/internal/control"
)

// RetryAuthenticationForTab authorizes one explicit request attempt for a
// credential that the current controller generation previously rejected. It
// never replays a failed turn.
func (a *App) RetryAuthenticationForTab(tabID string) (control.AuthenticationState, error) {
	a.runtimeRebuildMu.Lock()
	tab, ctrl := a.tabAndCtrlByID(tabID)
	defer func() {
		a.runtimeRebuildMu.Unlock()
		a.refreshTabMetaExtras(tab)
	}()
	if tab == nil {
		return control.AuthenticationState{}, fmt.Errorf("session is unavailable")
	}
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	if a.tabIsReadOnly(tab) {
		return control.AuthenticationState{}, readOnlyChannelErr()
	}
	if err := a.workspaceRuntimeAdmissionErr(tab, ctrl); err != nil {
		return control.AuthenticationState{}, err
	}
	retry, ok := ctrl.(interface {
		AuthenticationState() control.AuthenticationState
		RetryAuthentication() bool
	})
	if !ok {
		return control.AuthenticationState{}, fmt.Errorf("authentication retry is unavailable for this session")
	}
	state := retry.AuthenticationState()
	if !retry.RetryAuthentication() {
		return state, fmt.Errorf("authentication retry is only available after a rejected credential")
	}
	return retry.AuthenticationState(), nil
}
