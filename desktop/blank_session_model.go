package main

import (
	"fmt"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/control"
)

// alignReusableBlankTabModel makes a reused empty session obey the same
// provider/model default as a newly-created session. Ready runtimes use the
// normal failure-atomic model switch. A tab that is still starting has no
// controller to swap, so invalidate its startup generation, update the empty
// session's model metadata, and restart the build from the intended provider.
func (a *App) alignReusableBlankTabModel(tab *WorkspaceTab, model string) error {
	model = strings.TrimSpace(model)
	if tab == nil || model == "" {
		return nil
	}

	a.mu.RLock()
	if tab.removed || a.tabs[tab.ID] != tab {
		a.mu.RUnlock()
		return fmt.Errorf("blank session changed while applying the default model; retry")
	}
	currentModel := strings.TrimSpace(tab.model)
	ctrl := tab.Ctrl
	buildDone := tab.buildDone
	a.mu.RUnlock()

	if ctrl != nil {
		if currentModel != model {
			if err := a.SetModelForTab(tab.ID, model); err != nil {
				return err
			}
		}
		return a.ensureReusableBlankIdentity(tab)
	}
	if currentModel == model && buildDone != nil {
		// Reusing an in-flight blank must reuse its pending create, too. Cancelling
		// an identical build can leave durable content awaiting registry attach;
		// startup recovery would later publish that as a second blank session.
		select {
		case <-buildDone:
		case <-a.bootContext().Done():
			return a.bootContext().Err()
		}
		if err := a.ensureReusableBlankIdentity(tab); err != nil {
			return err
		}
		a.mu.RLock()
		ready := !tab.removed && a.tabs[tab.ID] == tab && tab.Ctrl != nil && tab.SessionID != ""
		startupErr := tab.StartupErr
		a.mu.RUnlock()
		if !ready {
			return fmt.Errorf("create session runtime: %s", startupErr)
		}
		return nil
	}

	// Fence the old build before publishing its replacement model. Legacy
	// sources stay read-only; the replacement commits the selected model as a
	// session/config event when it publishes its canonical identity.
	a.mu.Lock()
	if tab.removed || a.tabs[tab.ID] != tab {
		a.mu.Unlock()
		return fmt.Errorf("blank session changed while applying the default model; retry")
	}
	if tab.Ctrl != nil {
		a.mu.Unlock()
		return a.alignReusableBlankTabModel(tab, model)
	}
	a.supersedeTabBuildLocked(tab)
	tab.effort = config.RebindSessionEffort(nil, tab.model, model, tab.effort)
	tab.model = model
	tab.Label = model
	tab.Ready = false
	clearTabStartupError(tab)
	a.saveTabsLocked()
	a.mu.Unlock()
	a.buildTabController(tab)
	if err := a.ensureReusableBlankIdentity(tab); err != nil {
		return err
	}
	a.mu.RLock()
	ready := tab.Ctrl != nil && tab.SessionID != ""
	startupErr := tab.StartupErr
	a.mu.RUnlock()
	if !ready {
		return fmt.Errorf("create session runtime: %s", startupErr)
	}
	return nil
}

// Reusing an empty historical shell is still a New operation. It must bind a
// current-store identity rather than making the next user turn a legacy write.
func (a *App) ensureReusableBlankIdentity(tab *WorkspaceTab) error {
	release := a.lockRuntimeMutation("create reusable blank session")
	defer release()
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	ctrl := a.controllerForTab(tab)
	if ctrl == nil {
		return nil // The caller reports the recorded startup failure.
	}
	identity, ok := ctrl.(control.IdentityLifecycle)
	if !ok || identity.SessionService() == nil {
		return nil
	}
	if _, bound := identity.SessionRef(); bound && identity.UsesExclusiveSession() && identity.SessionService() == a.desktopSessionService("") {
		return nil
	}
	if controllerHasActiveRuntimeWork(ctrl) || messagesHaveConversationContent(ctrl.History()) {
		return fmt.Errorf("blank session changed before creation; retry")
	}
	if err := ctrl.NewSession(); err != nil {
		return err
	}
	a.syncTabSessionIdentity(tab, ctrl)
	return nil
}
