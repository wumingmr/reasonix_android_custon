package main

import (
	"fmt"

	"reasonix/internal/control"
)

type imageCapabilitySnapshot interface{ ImageCapabilityChanged() bool }

// Use the existing build/swap/lease boundary before accepting a new turn.
// A failed rebuild leaves the previous snapshot visible and rejects this turn.
func (a *App) refreshTabImageCapability(tab *WorkspaceTab) error {
	a.runtimeRebuildMu.Lock()
	defer a.runtimeRebuildMu.Unlock()
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	current, ok := a.controllerForTab(tab).(imageCapabilitySnapshot)
	if !ok || !current.ImageCapabilityChanged() {
		return nil
	}
	if err := a.rebuildSettingTurnLocked("image input", tab, false, false); err != nil {
		return fmt.Errorf("refresh image input configuration: %w", err)
	}
	return nil
}

// tabTurnAdmission owns both locks acquired while a foreground turn starts.
type tabTurnAdmission struct {
	app      *App
	tab      *WorkspaceTab
	released bool
}

type turnFinishingWaiter interface {
	TurnFinishingDone() (<-chan struct{}, bool)
}

func (admission *tabTurnAdmission) finish(ctrl control.SessionAPI) bool {
	if admission == nil || admission.released {
		return false
	}
	admission.released = true
	tab := admission.tab
	if tab != nil {
		// Defers preserve lock release if RuntimeStatus panics.
		defer admission.app.runtimeAdmissionMu.RUnlock()
		defer tab.turnStartMu.Unlock()
	}
	started := ctrl != nil && ctrl.RuntimeStatus().Running
	if !started && tab != nil && tab.sink != nil {
		tab.sink.cancelTurnStart()
	}
	return started
}

func (admission *tabTurnAdmission) abort() {
	admission.finish(nil)
}

// beginTabTurn reserves one tab until its TurnDone fan-out completes.
func (a *App) beginTabTurn(tabID string, reclaim bool, submissionID ...string) (*tabTurnAdmission, control.SessionAPI, error) {
	return a.beginRuntimeTurnChecked(tabID, reclaim, false, identifiedSubmissionCheck(submissionID), submissionID...)
}

func (a *App) beginRuntimeTurn(tabID string, reclaim, detached bool, submissionID ...string) (*tabTurnAdmission, control.SessionAPI, error) {
	return a.beginRuntimeTurnChecked(tabID, reclaim, detached, nil, submissionID...)
}

func checkRuntimeAdmission(ctrl control.SessionAPI, check func(control.SessionAPI) error) error {
	if check == nil {
		return nil
	}
	return check(ctrl)
}

func controllerAuthenticationError(ctrl control.SessionAPI) error {
	authentication, ok := ctrl.(interface {
		AuthenticationState() control.AuthenticationState
	})
	if !ok {
		return nil
	}
	state := authentication.AuthenticationState()
	if state.Ready() {
		return nil
	}
	return &control.AuthenticationError{State: state}
}

// check observes the selected controller under the same admission locks as the
// running check and submit. A refusal must carry this owner's identity with it.
func (a *App) beginRuntimeTurnChecked(tabID string, reclaim, detached bool, check func(control.SessionAPI) error, submissionID ...string) (*tabTurnAdmission, control.SessionAPI, error) {
	return a.beginRuntimeTurnWithModelChoice(tabID, reclaim, detached, check, nil, submissionID...)
}

func (a *App) beginRuntimeTurnWithModelChoice(tabID string, reclaim, detached bool, check func(control.SessionAPI) error, choice *control.ModelApplicationChoice, submissionID ...string) (*tabTurnAdmission, control.SessionAPI, error) {
	for {
		tab, ctrl := a.tabAndCtrlByID(tabID)
		if detached {
			a.mu.RLock()
			tab = a.tabByEventSinkIDLocked(tabID)
			ctrl = nil
			if tab != nil {
				ctrl = tab.Ctrl
			}
			a.mu.RUnlock()
		}
		if a.tabIsReadOnly(tab) {
			return nil, nil, readOnlyChannelErr()
		}
		if err := a.workspaceRuntimeAdmissionErr(tab, ctrl); err != nil {
			return nil, nil, err
		}
		// Slow workspace repair stays outside the runtime admission barrier.
		if err := a.ensureTabControllerWorkspace(tab); err != nil {
			return nil, nil, err
		}

		a.runtimeAdmissionMu.RLock()
		abort := func() {
			tab.turnStartMu.Unlock()
			a.runtimeAdmissionMu.RUnlock()
		}
		tab.turnStartMu.Lock()
		if err := a.validateDraftAdmission(tab, firstSubmissionID(submissionID)); err != nil {
			abort()
			return nil, nil, err
		}
		if a.tabIsReadOnly(tab) {
			abort()
			return nil, nil, readOnlyChannelErr()
		}
		if reclaim && a.botBridge != nil {
			a.botBridge.reclaimFromDesktop(tab.ID)
		}
		ctrl = a.controllerForTab(tab)
		if err := a.workspaceRuntimeAdmissionErr(tab, ctrl); err != nil {
			abort()
			return nil, nil, err
		}
		ctrl = a.controllerForTab(tab)
		if err := a.workspaceRuntimeAdmissionErr(tab, ctrl); err != nil {
			abort()
			return nil, nil, err
		}
		running := ctrl.RuntimeStatus().Running
		if err := checkRuntimeAdmission(ctrl, check); err != nil {
			abort()
			return nil, nil, err
		}
		if running {
			if waiter, ok := ctrl.(turnFinishingWaiter); ok {
				if done, finishing := waiter.TurnFinishingDone(); finishing {
					// Re-resolve after waiting so close/switch cannot misroute retry.
					abort()
					<-done
					continue
				}
				// Fan-out can end between RuntimeStatus and TurnFinishingDone.
				// Re-check before reporting busy so that completed boundary retries
				// instead of preserving the original false rejection window.
				if !ctrl.RuntimeStatus().Running {
					abort()
					continue
				}
			}
			abort()
			return nil, nil, control.ErrTurnRunning
		}
		usingApplied, choiceErr := validateModelApplicationChoice(ctrl, choice)
		if choiceErr != nil {
			abort()
			return nil, nil, choiceErr
		}
		if retry, err := a.applyTurnModelSettings(tab, ctrl, usingApplied, abort); retry || err != nil {
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		if snapshot, ok := ctrl.(imageCapabilitySnapshot); a.ctx != nil && !usingApplied && ok && snapshot.ImageCapabilityChanged() {
			a.mu.RLock()
			draftPending := tab.PendingCreateOperationID != ""
			a.mu.RUnlock()
			abort()
			if draftPending {
				return nil, nil, fmt.Errorf("image configuration changed before draft admission")
			}
			if err := a.refreshTabImageCapability(tab); err != nil {
				return nil, nil, err
			}
			continue
		}
		if err := controllerAuthenticationError(ctrl); err != nil {
			abort()
			return nil, nil, err
		}
		if tab.sink != nil && !tab.sink.tryBeginTurn(submissionID...) {
			abort()
			return nil, nil, control.ErrTurnRunning
		}
		if metadata, ok := ctrl.(interface {
			SetTurnEventRoutingMetadata(runtimeEpoch, submissionID string)
		}); ok {
			epoch := ""
			if tab.sink != nil {
				epoch = tab.sink.runtimeEpochSnapshot()
			}
			metadata.SetTurnEventRoutingMetadata(epoch, firstSubmissionID(submissionID))
		}
		return &tabTurnAdmission{app: a, tab: tab}, ctrl, nil
	}
}

// Durable follow-ups are new runs even when a controller dispatches them on
// its own after TurnDone. Resolve the runtime owner again after detach/attach.
func (a *App) beforeInboxDispatch(ctrl *control.Controller) (func(), error) {
	a.mu.RLock()
	var owner *WorkspaceTab
	for _, tab := range a.runtimeTabsLocked() {
		if tab.Ctrl == ctrl {
			owner = tab
			break
		}
	}
	a.mu.RUnlock()
	if owner == nil {
		return nil, control.ErrInboxRuntimeUnpublished
	}
	admission, current, err := a.beginRuntimeTurn(owner.ID, false, true)
	if err != nil {
		return nil, err
	}
	if current != ctrl {
		admission.abort()
		if replacement, ok := current.(*control.Controller); ok {
			go replacement.NotifyInboxRuntimeReady()
		}
		return nil, control.ErrInboxRuntimeUnpublished
	}
	return func() { admission.finish(ctrl) }, nil
}
