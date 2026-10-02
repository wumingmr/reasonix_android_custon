package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"reasonix/internal/control"
)

// Once a source-bound Serve supports the enhanced protocol, it owns refresh
// and receipt lookup in one admission transaction. Desktop must not reject a
// retry before Serve can return an already accepted receipt.
func (a *App) remoteModelApplicationReady(tabID string) bool {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	t := a.remoteTabs[tabID]
	return t != nil && t.capabilities["model-application-v1"] && t.settings.revision != "" && t.settings.generation == t.gen && t.settings.sessionPath == t.routing.currentPath
}

func (a *App) remoteModelApplicationDetails(tabID string) *ModelApplicationDetails {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	if tab == nil || !tab.capabilities["model-application-v1"] {
		a.remoteTabMu.Unlock()
		return nil
	}
	generation := tab.gen
	a.remoteTabMu.Unlock()
	client, base, path, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return nil
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	status, err := remoteModelSettingsRequest(ctx, client, base, path, nil)
	if err != nil {
		return nil
	}
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	if current := a.remoteTabs[tabID]; current != tab || current.gen != generation || current.routing.currentPath != path {
		return nil
	}
	return bindRemoteModelConfirmation(tab, generation, path, status.Application)
}

// A host token prevents a confirmation from being revived by a later status
// refresh after reconnect, even if Serve still has the same model runtime.
func bindRemoteModelConfirmation(tab *remoteTab, generation uint64, path string, detail *ModelApplicationDetails) *ModelApplicationDetails {
	if detail == nil {
		return nil
	}
	copy := *detail
	copy.ConfirmationToken = "" // Only this Desktop connection can issue confirmations.
	previous := tab.settings.details
	if previous != nil && tab.settings.detailsGeneration == generation && tab.settings.detailsPath == path && previous.RuntimeIdentity == copy.RuntimeIdentity && previous.AppliedRevision == copy.AppliedRevision && previous.DesiredRevision == copy.DesiredRevision {
		copy.ConfirmationToken = previous.ConfirmationToken
	}
	if copy.ConfirmationToken == "" {
		copy.ConfirmationToken = rand.Text()
	}
	tab.settings.details = &copy
	tab.settings.detailsGeneration, tab.settings.detailsPath = generation, path
	return &copy
}

func (a *App) validateRemoteModelConfirmation(tabID string, choice control.ModelApplicationChoice) error {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	tab := a.remoteTabs[tabID]
	if tab == nil || !tab.capabilities["model-application-v1"] || tab.settings.detailsGeneration != tab.gen || tab.settings.detailsPath != tab.routing.currentPath {
		return &submissionNotAcceptedError{cause: control.ErrModelChoiceStale}
	}
	d := tab.settings.details
	if d == nil || choice.ConfirmationToken == "" || choice.ConfirmationToken != d.ConfirmationToken || choice.ExpectedRuntimeIdentity != d.RuntimeIdentity || choice.ExpectedAppliedRevision != d.AppliedRevision || choice.ExpectedDesiredRevision != d.DesiredRevision {
		return &submissionNotAcceptedError{cause: control.ErrModelChoiceStale}
	}
	return nil
}

func (a *App) SubmitRemoteTabWithModelApplication(tabID, text, submissionID string, choice control.ModelApplicationChoice) error {
	if err := a.requireRemoteExecutionProtocol(tabID); err != nil {
		return err
	}
	if err := a.requireRemotePermissionPresets(tabID); err != nil {
		return err
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	supported := tab != nil && tab.capabilities["model-application-v1"]
	var generation uint64
	if tab != nil {
		generation = tab.gen
	}
	a.remoteTabMu.Unlock()
	if !supported {
		return &submissionNotAcceptedError{cause: fmt.Errorf("upgrade Serve to use model application recovery")}
	}
	_, _, path, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	if !a.remoteTabAdmissionCurrent(tabID, generation) {
		return &submissionNotAcceptedError{cause: control.ErrModelChoiceStale}
	}
	if choice.Mode == "applied_once" {
		if err := a.validateRemoteModelConfirmation(tabID, choice); err != nil {
			// Serve must still be allowed to acknowledge an existing receipt.
			// Empty identity can never authorize new execution; a missing
			// receipt therefore returns its structured stale-choice rejection.
			choice.ExpectedRuntimeIdentity = ""
		}
	}
	body, err := json.Marshal(map[string]any{"input": text, "submissionId": submissionID, "modelApplication": choice})
	if err != nil {
		return err
	}
	err = a.submitWithRouteRetry(tabID, func(client *http.Client, base, expectedPath string) error {
		ctx, cancel := commandContext(a)
		defer cancel()
		return servePostForSession(ctx, client, serveURL(base, "/submit"), body, expectedPath)
	})
	var detailed interface{ RPCErrorData() map[string]any }
	if errors.As(err, &detailed) {
		if raw, marshalErr := json.Marshal(detailed.RPCErrorData()["modelApplication"]); marshalErr == nil {
			var detail *ModelApplicationDetails
			if json.Unmarshal(raw, &detail) == nil && detail != nil {
				a.remoteTabMu.Lock()
				if current := a.remoteTabs[tabID]; current == tab && current.gen == generation && current.routing.currentPath == path {
					detail = bindRemoteModelConfirmation(current, generation, path, detail)
					var httpError *serveHTTPStatusError
					if errors.As(err, &httpError) {
						httpError.data["modelApplication"] = detail
					}
				}
				a.remoteTabMu.Unlock()
			}
		}
	}
	if remoteBusySubmitError(err) {
		// Busy submissions become durable follow-ups. The queue does not carry
		// the per-submit model application choice; the message remains queued.
		if queued, queueErr := a.queueBusyFollowup(tabID, text, submissionID); queueErr == nil {
			return queued
		}
	}
	return err
}
