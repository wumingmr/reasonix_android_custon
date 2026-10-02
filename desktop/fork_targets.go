package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

type forkTargetsController interface {
	ForkTargets() (session.ForkTargetSet, error)
	CreateForkSession(request session.ForkRequest, name string) (string, error)
}

var _ forkTargetsController = (*control.Controller)(nil)

type ForkAnchorView struct {
	SourceHostID      string `json:"sourceHostId,omitempty"`
	SourceSessionID   string `json:"sourceSessionId"`
	SessionGeneration uint64 `json:"sessionGeneration"`
	TurnID            string `json:"turnId"`
	BoundarySequence  uint64 `json:"boundarySequence"`
}

type ForkTargetView struct {
	SourceHostID      string `json:"sourceHostId,omitempty"`
	SourceSessionID   string `json:"sourceSessionId"`
	SessionGeneration uint64 `json:"sessionGeneration"`
	TurnID            string `json:"turnId"`
	BoundarySequence  uint64 `json:"boundarySequence"`
	TurnNumber        int    `json:"turnNumber"`
	Status            string `json:"status"`
	MessageID         string `json:"messageId,omitempty"`
	Available         bool   `json:"available"`
	Reason            string `json:"reason,omitempty"`
}

type ForkTargetSetView struct {
	SourceHostID      string           `json:"sourceHostId,omitempty"`
	SourceSessionID   string           `json:"sourceSessionId,omitempty"`
	SessionGeneration uint64           `json:"sessionGeneration,omitempty"`
	Targets           []ForkTargetView `json:"targets"`
	Verifiable        bool             `json:"verifiable"`
}

type ForkCreationView struct {
	SessionID   string `json:"sessionId,omitempty"`
	TabID       string `json:"tabId,omitempty"`
	OperationID string `json:"operationId,omitempty"`
	Opened      bool   `json:"opened"`
	Code        string `json:"code,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Error       string `json:"error,omitempty"`
}

func forkTargetSetView(set session.ForkTargetSet, generation uint64) ForkTargetSetView {
	view := ForkTargetSetView{SourceHostID: set.Source.HostID, SourceSessionID: set.Source.SessionID,
		SessionGeneration: generation, Targets: make([]ForkTargetView, 0, len(set.Targets)), Verifiable: set.Verifiable}
	for _, target := range set.Targets {
		view.Targets = append(view.Targets, ForkTargetView{
			SourceHostID: set.Source.HostID, SourceSessionID: set.Source.SessionID, SessionGeneration: generation,
			TurnID: target.TurnID, BoundarySequence: target.BoundarySequence,
			TurnNumber: target.TurnNumber, Status: string(target.Status), MessageID: target.MessageID,
			Available: target.Available, Reason: string(target.Reason)})
	}
	return view
}

func (a *App) ForkTargetsForTab(tabID string) (ForkTargetSetView, error) {
	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil || tab.Ctrl == nil {
		a.mu.RUnlock()
		return ForkTargetSetView{Targets: []ForkTargetView{}}, nil
	}
	ctrl, generation := tab.Ctrl, tab.SessionGeneration
	a.mu.RUnlock()
	targets, ok := ctrl.(forkTargetsController)
	if !ok {
		return ForkTargetSetView{Targets: []ForkTargetView{}}, nil
	}
	set, err := targets.ForkTargets()
	if err != nil {
		return ForkTargetSetView{Targets: []ForkTargetView{}}, err
	}
	a.mu.RLock()
	current := a.tabs[tab.ID]
	stale := current != tab || current.Ctrl != ctrl || current.SessionGeneration != generation ||
		(strings.TrimSpace(current.SessionID) != "" && current.SessionID != set.Source.SessionID)
	a.mu.RUnlock()
	if stale {
		return ForkTargetSetView{Targets: []ForkTargetView{}}, &session.ForkUnavailableError{Reason: session.ForkStaleSource}
	}
	return forkTargetSetView(set, generation), nil
}

func (a *App) localForkSource(tabID string, anchor ForkAnchorView) (*WorkspaceTab, forkTargetsController, session.SessionRef, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil || tab.Ctrl == nil {
		return nil, nil, session.SessionRef{}, fmt.Errorf("fork source tab is unavailable")
	}
	creator, ok := tab.Ctrl.(forkTargetsController)
	if !ok {
		return nil, nil, session.SessionRef{}, fmt.Errorf("fork is unsupported")
	}
	ref := session.SessionRef{HostID: strings.TrimSpace(anchor.SourceHostID), SessionID: strings.TrimSpace(anchor.SourceSessionID)}
	if ref.SessionID == "" || tab.SessionID != ref.SessionID || tab.SessionGeneration != anchor.SessionGeneration {
		return nil, nil, session.SessionRef{}, &session.ForkUnavailableError{TurnID: anchor.TurnID, Reason: session.ForkStaleSource}
	}
	return tab, creator, ref, nil
}

func (a *App) CreateForkForTab(tabID string, anchor ForkAnchorView) (ForkCreationView, error) {
	tab, creator, source, err := a.localForkSource(tabID, anchor)
	if err != nil {
		return forkRefusalView(err), nil
	}
	operation, err := a.beginForkOperation(forkOperation{Surface: "local", TabID: tab.ID,
		SourceHostID: source.HostID, SourceSessionID: source.SessionID,
		TurnID: strings.TrimSpace(anchor.TurnID), BoundarySequence: anchor.BoundarySequence})
	if err != nil {
		return ForkCreationView{}, err
	}
	childID := operation.ChildSessionID
	if operation.State != "completed" || childID == "" {
		// Keep the source tab stable through the controller's source snapshot.
		// Otherwise a reset could detach it after the App check and let the old
		// controller create a child for a tab showing another session.
		a.mu.RLock()
		current := a.tabs[tab.ID]
		var boundCreator forkTargetsController
		controllerMatches := false
		if current != nil {
			boundCreator, controllerMatches = current.Ctrl.(forkTargetsController)
		}
		if current != tab || !controllerMatches || boundCreator != creator || current.SessionID != source.SessionID ||
			current.SessionGeneration != anchor.SessionGeneration {
			a.mu.RUnlock()
			_ = a.discardForkOperation(operation.OperationID)
			return forkRefusalView(&session.ForkUnavailableError{TurnID: anchor.TurnID, Reason: session.ForkStaleSource}), nil
		}
		childID, err = creator.CreateForkSession(session.ForkRequest{Source: source, TurnID: operation.TurnID,
			BoundarySequence: operation.BoundarySequence, OperationID: operation.OperationID}, "")
		a.mu.RUnlock()
		if err != nil {
			var unavailable *session.ForkUnavailableError
			if errors.As(err, &unavailable) {
				_ = a.discardForkOperation(operation.OperationID)
				return forkRefusalView(err), nil
			}
			return ForkCreationView{}, err
		}
		if err := a.completeForkOperation(operation.OperationID, childID); err != nil {
			return ForkCreationView{}, err
		}
	}
	view := ForkCreationView{SessionID: childID, OperationID: operation.OperationID}
	a.mu.RLock()
	for _, existing := range a.tabs {
		if existing != nil && existing.SessionID == childID {
			view.TabID, view.Opened = existing.ID, true
			break
		}
	}
	a.mu.RUnlock()
	if view.Opened {
		return view, nil
	}
	if err := a.attachForkedDesktopSession(a.bootContext(), tab, childID); err != nil {
		return ForkCreationView{}, fmt.Errorf("publish fork workspace membership: %w", err)
	}
	opened, openErr := a.openForkedSessionTabWithWorkspace(tab, forkedSessionLocator{SessionID: childID}, "")
	view.TabID = opened.tab.ID
	if openErr == nil && opened.tab.ID != "" {
		view.Opened = true
		return view, nil
	}
	if openErr != nil {
		slog.Warn("fork: child session created but tab attach failed", "session", childID, "err", openErr)
	}
	view.Error = rewindForkAttachError
	return view, nil
}

func forkRefusalView(err error) ForkCreationView {
	var unavailable *session.ForkUnavailableError
	if errors.As(err, &unavailable) {
		return ForkCreationView{Code: "fork_unavailable", Reason: string(unavailable.Reason), Error: unavailable.Error()}
	}
	return ForkCreationView{Error: err.Error()}
}
