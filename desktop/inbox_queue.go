package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"reasonix/internal/control"
	"reasonix/internal/servecontract"
	"reasonix/internal/sessioninbox"
)

type InboxQueueResultView struct {
	Outcome  string                     `json:"outcome"`
	Reason   string                     `json:"reason,omitempty"`
	Snapshot InboxSnapshotView          `json:"snapshot"`
	Edit     *control.InboxQueueEdit    `json:"edit,omitempty"`
	Receipt  *sessioninbox.InboxReceipt `json:"receipt,omitempty"`
}

// InboxQueueForTarget is additive and never falls back from remote to local.
func (a *App) InboxQueueForTarget(target InboxTargetView, request control.InboxQueueRequest) (InboxQueueResultView, error) {
	if target.Remote {
		return a.remoteInboxQueue(target, request)
	}
	a.runtimeAdmissionMu.RLock()
	defer a.runtimeAdmissionMu.RUnlock()
	current, err := a.captureInboxTarget(target.TabID, target.SessionPath)
	if err != nil || current != target {
		return InboxQueueResultView{Outcome: "unavailable", Reason: "session_changed"}, nil
	}
	ctrl, err := a.inboxCtrl(target.TabID)
	if err != nil {
		return InboxQueueResultView{}, err
	}
	api, ok := ctrl.(interface {
		InboxQueue(string, control.InboxQueueRequest) (control.InboxQueueResult, error)
	})
	if !ok {
		return InboxQueueResultView{Outcome: "unavailable", Reason: "unsupported"}, nil
	}
	result, err := api.InboxQueue(target.SessionPath, request)
	if err != nil {
		return InboxQueueResultView{}, inboxBridgeError(err)
	}
	return inboxQueueView(result), nil
}

func inboxQueueView(result control.InboxQueueResult) InboxQueueResultView {
	return InboxQueueResultView{Outcome: result.Outcome, Reason: result.Reason, Snapshot: inboxSnapshotView(result.Snapshot), Edit: result.Edit, Receipt: result.Receipt}
}

func (a *App) remoteInboxQueue(target InboxTargetView, request control.InboxQueueRequest) (InboxQueueResultView, error) {
	client, base, err := a.remoteInboxTarget(target)
	if err != nil {
		return InboxQueueResultView{Outcome: "unavailable", Reason: "session_changed"}, nil
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[target.TabID]
	supported := tab != nil && tab.capabilities[servecontract.InboxMutationsV1]
	a.remoteTabMu.Unlock()
	if !supported {
		return InboxQueueResultView{Outcome: "unavailable", Reason: "unsupported"}, nil
	}
	body, err := json.Marshal(map[string]any{"sessionPath": target.SessionPath, "request": request})
	if err != nil {
		return InboxQueueResultView{}, err
	}
	ctx, cancel := commandContext(a)
	defer cancel()
	response, err := serveDoForSession(ctx, client, http.MethodPost, serveURL(base, "/inbox/queue"), body, target.SessionPath)
	if err != nil {
		return InboxQueueResultView{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return InboxQueueResultView{Outcome: "unavailable", Reason: "session_changed"}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return InboxQueueResultView{}, fmt.Errorf("remote inbox operation failed (%d)", response.StatusCode)
	}
	var result control.InboxQueueResult
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&result); err != nil {
		return InboxQueueResultView{}, err
	}
	currentClient, currentBase, err := a.remoteInboxTarget(target)
	if err != nil || currentClient != client || currentBase != base {
		return InboxQueueResultView{Outcome: "unavailable", Reason: "session_changed"}, nil
	}
	return inboxQueueView(result), nil
}
