package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func (a *App) forkTargetsCapable(tabID string) bool {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	return remoteForkTargetsSupported(a.remoteTabs[tabID])
}

func remoteForkTargetsSupported(tab *remoteTab) bool {
	return tab != nil && tab.capabilities[serveCapabilitySessionForkTargetsV1]
}

const remoteForkUnsupported = "this remote Reasonix Serve does not support " + serveCapabilitySessionForkTargetsV1 + "; upgrade it to fork a remote turn into an independent session"

func (a *App) ForkTargetsRemoteTab(tabID string) (ForkTargetSetView, error) {
	empty := ForkTargetSetView{Targets: []ForkTargetView{}}
	if !a.forkTargetsCapable(tabID) {
		return empty, nil
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	if tab == nil || tab.client == nil || tab.state != "ready" || tab.routing.currentPath == "" {
		a.remoteTabMu.Unlock()
		return empty, fmt.Errorf("remote tab %q is unavailable", tabID)
	}
	client, base, expectedPath, generation := tab.client, tab.base, tab.routing.currentPath, tab.gen
	a.remoteTabMu.Unlock()
	ctx, cancel := commandContext(a)
	defer cancel()
	resp, err := serveDoForSession(ctx, client, http.MethodGet, serveURL(base, "/fork-targets"), nil, expectedPath)
	if err != nil {
		return empty, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, serveSnapshotMaxBytes+1))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return empty, &serveHTTPStatusError{url: serveURL(base, "/fork-targets"), statusCode: resp.StatusCode, message: strings.TrimSpace(string(body))}
	}
	var wire struct {
		Source struct {
			HostID    string `json:"hostId"`
			SessionID string `json:"sessionId"`
		} `json:"source"`
		Targets []struct {
			TurnID           string `json:"turnId"`
			BoundarySequence uint64 `json:"boundarySequence"`
			TurnNumber       int    `json:"turnNumber"`
			Status           string `json:"status"`
			MessageID        string `json:"messageId"`
			Available        bool   `json:"available"`
			Reason           string `json:"reason"`
		} `json:"targets"`
		Verifiable bool `json:"verifiable"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return empty, fmt.Errorf("decode remote fork targets: %w", err)
	}
	a.remoteTabMu.Lock()
	current := a.remoteTabs[tabID]
	stale := current != tab || current.client != client || current.base != base || current.gen != generation || current.routing.currentPath != expectedPath
	a.remoteTabMu.Unlock()
	expectedID, hasID := strings.CutPrefix(expectedPath, remoteSessionIDRoutePrefix)
	if stale || !hasID || strings.TrimSpace(wire.Source.SessionID) == "" || wire.Source.SessionID != expectedID {
		return empty, fmt.Errorf("stale_source")
	}
	view := ForkTargetSetView{SourceHostID: wire.Source.HostID, SourceSessionID: wire.Source.SessionID,
		SessionGeneration: generation, Targets: make([]ForkTargetView, 0, len(wire.Targets)), Verifiable: wire.Verifiable}
	for _, target := range wire.Targets {
		view.Targets = append(view.Targets, ForkTargetView{SourceHostID: wire.Source.HostID,
			SourceSessionID: wire.Source.SessionID, SessionGeneration: generation, TurnID: target.TurnID,
			BoundarySequence: target.BoundarySequence, TurnNumber: target.TurnNumber, Status: target.Status,
			MessageID: target.MessageID, Available: target.Available, Reason: target.Reason})
	}
	return view, nil
}

func (a *App) CreateForkRemoteTab(tabID string, anchor ForkAnchorView) (ForkCreationView, error) {
	if strings.TrimSpace(anchor.TurnID) == "" {
		return ForkCreationView{}, fmt.Errorf("forking a remote turn requires a turn id")
	}
	if !a.forkTargetsCapable(tabID) {
		return ForkCreationView{Code: "fork_unavailable", Reason: "unsupported", Error: remoteForkUnsupported}, nil
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	currentID := ""
	if tab != nil {
		currentID, _ = strings.CutPrefix(tab.routing.currentPath, remoteSessionIDRoutePrefix)
	}
	stale := tab == nil || tab.gen != anchor.SessionGeneration || currentID != strings.TrimSpace(anchor.SourceSessionID)
	a.remoteTabMu.Unlock()
	if stale {
		return ForkCreationView{Code: "fork_unavailable", Reason: "stale_source", Error: "fork source session changed"}, nil
	}
	operation, err := a.beginForkOperation(forkOperation{Surface: "remote", TabID: tabID,
		SourceHostID: anchor.SourceHostID, SourceSessionID: anchor.SourceSessionID,
		TurnID: strings.TrimSpace(anchor.TurnID), BoundarySequence: anchor.BoundarySequence})
	if err != nil {
		return ForkCreationView{}, err
	}
	if operation.State == "completed" && operation.ChildSessionID != "" {
		return ForkCreationView{SessionID: operation.ChildSessionID, OperationID: operation.OperationID, Opened: true}, nil
	}
	var view ForkCreationView
	err = a.remoteTabPostJSON(tabID, "/fork-session", map[string]any{
		"sourceSessionId": operation.SourceSessionID, "turnId": operation.TurnID,
		"boundarySequence": operation.BoundarySequence, "name": "", "operationId": operation.OperationID,
	}, &view)
	if err != nil {
		var statusErr *serveHTTPStatusError
		if errors.As(err, &statusErr) {
			var refusal struct{ Code, Reason, Message string }
			if json.Unmarshal([]byte(statusErr.message), &refusal) == nil && refusal.Code != "" {
				if statusErr.statusCode >= 400 && statusErr.statusCode < 500 {
					_ = a.discardForkOperation(operation.OperationID)
				}
				return ForkCreationView{Code: refusal.Code, Reason: refusal.Reason, Error: refusal.Message}, nil
			}
			if statusErr.statusCode >= 400 && statusErr.statusCode < 500 {
				_ = a.discardForkOperation(operation.OperationID)
			}
			return ForkCreationView{Error: statusErr.message}, nil
		}
		return ForkCreationView{}, err
	}
	view.OperationID = operation.OperationID
	if strings.TrimSpace(view.SessionID) == "" {
		return ForkCreationView{}, fmt.Errorf("remote fork response has no child session id")
	}
	if err := a.completeForkOperation(operation.OperationID, view.SessionID); err != nil {
		return ForkCreationView{}, err
	}
	view.Opened = true
	return view, nil
}
