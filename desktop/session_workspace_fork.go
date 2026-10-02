package main

import (
	"context"
	"errors"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
	"slices"
	"strings"
)

func (a *App) ForkSession(ref session.SessionRef, turnBoundary string) (session.SessionRef, error) {
	plan, err := a.planCanonicalFork(ref, turnBoundary)
	if err != nil {
		return session.SessionRef{}, err
	}
	operationID := "fork-" + strings.TrimPrefix(newTabID(), "tab_")
	childID := "desktop-" + strings.TrimPrefix(newTabID(), "tab_")
	return a.executeCanonicalFork(ref, plan, operationID, childID)
}

type canonicalForkPlan struct {
	workspaceID      string
	beforeID         string
	sourceGeneration uint64
	target           session.ForkTarget
}

func (a *App) planCanonicalFork(ref session.SessionRef, turnBoundary string) (canonicalForkPlan, error) {
	if err := validateLocalSessionRef(ref); err != nil {
		return canonicalForkPlan{}, err
	}
	state, err := a.workspaceRegistry().Load(context.Background())
	if err != nil {
		return canonicalForkPlan{}, err
	}
	workspaceID, beforeID := "", ""
	for _, id := range state.WorkspaceIDs {
		workspace := state.Workspaces[id]
		for index, sessionID := range workspace.SessionIDs {
			if sessionID != ref.SessionID {
				continue
			}
			workspaceID = id
			if index+1 < len(workspace.SessionIDs) {
				beforeID = workspace.SessionIDs[index+1]
			}
			break
		}
		if workspaceID != "" {
			break
		}
	}
	if workspaceID == "" {
		return canonicalForkPlan{}, workspacestate.ErrSessionNotFound
	}
	sourceState := state.SessionStates[ref.SessionID]
	if sourceState.Lifecycle != workspacestate.Active {
		return canonicalForkPlan{}, workspacestate.ErrMutationConflict
	}
	targets, err := a.desktopSessionService("").ForkTargetSetFor(a.bootContext(), ref)
	if err != nil {
		return canonicalForkPlan{}, err
	}
	turnBoundary = strings.TrimSpace(turnBoundary)
	var selected session.ForkTarget
	if turnBoundary == "" {
		for _, target := range slices.Backward(targets.Targets) {
			if target.Available {
				selected = target
				break
			}
		}
		if selected.TurnID == "" {
			return canonicalForkPlan{}, errors.New("session has no completed turn to fork")
		}
	} else {
		for _, target := range targets.Targets {
			if target.TurnID == turnBoundary {
				selected = target
				break
			}
		}
		if selected.TurnID == "" || !selected.Available {
			reason := selected.Reason
			if reason == "" {
				reason = session.ForkHistoryUnverifiable
			}
			return canonicalForkPlan{}, &session.ForkUnavailableError{TurnID: turnBoundary, Reason: reason}
		}
	}
	return canonicalForkPlan{
		workspaceID: workspaceID, beforeID: beforeID,
		sourceGeneration: sourceState.Generation, target: selected,
	}, nil
}

func (a *App) executeCanonicalFork(ref session.SessionRef, plan canonicalForkPlan, operationID, childID string) (session.SessionRef, error) {
	operationID = strings.TrimSpace(operationID)
	childID = strings.TrimSpace(childID)
	if operationID == "" || childID == "" {
		return session.SessionRef{}, errors.New("fork operation and child identities are required")
	}
	stateBefore, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return session.SessionRef{}, err
	}
	w := stateBefore.Workspaces[plan.workspaceID]
	scope := "project"
	if plan.workspaceID == workspacestate.GlobalWorkspaceID {
		scope = "global"
	}
	if _, _, err := a.ensureSessionOrganization(scope, w.Root); err != nil {
		return session.SessionRef{}, err
	}
	if err := a.workspaceRegistry().BeginCreate(a.bootContext(), workspacestate.PendingCreate{
		OperationID: operationID, WorkspaceID: plan.workspaceID, SessionID: childID, ParentSessionID: ref.SessionID,
	}); err != nil {
		return session.SessionRef{}, err
	}
	forked, err := a.desktopSessionService("").CreateFork(a.bootContext(), session.ForkRequest{
		Source: ref, TurnID: plan.target.TurnID, BoundarySequence: plan.target.BoundarySequence,
		OperationID: operationID, ChildID: childID,
	})
	if err != nil {
		_ = a.workspaceRegistry().AbortCreate(context.Background(), childID)
		return session.SessionRef{}, err
	}
	if _, already := stateBefore.SessionStates[childID]; !already {
		// Forks are ordinary independent sessions. Give the child its own title and
		// presentation while retaining ParentSessionID in the session header.
		parentTitle := ""
		if infos, infoErr := listWorkspaceSessionInfo(a.bootContext(), a.desktopSessionService("").Query(), []string{ref.SessionID}); infoErr == nil {
			parentTitle = strings.TrimSpace(infos[ref.SessionID].Title)
		}
		if parentTitle == "" {
			if state, stateErr := a.workspaceRegistry().Load(a.bootContext()); stateErr == nil {
				parentTitle = strings.TrimSpace(state.Presentation[ref.SessionID].Title)
			}
		}
		if parentTitle == "" {
			parentTitle = a.localizedDefaultTopicTitle()
		}
		forkTitle := parentTitle + a.localizedForkTitleSuffix()
		if err := a.desktopSessionService("").SetTitle(a.bootContext(), forked.Child, forkTitle); err != nil {
			return session.SessionRef{}, err
		}
		if err := a.workspaceRegistry().PrepareCreatePresentation(a.bootContext(), childID, workspacestate.Presentation{Title: forkTitle, SortOrder: -1}); err != nil {
			return session.SessionRef{}, err
		}
	}
	if err := a.workspaceRegistry().AttachSessionFromSourceIfUnchanged(
		a.bootContext(),
		operationID,
		plan.workspaceID,
		childID,
		plan.beforeID,
		ref.SessionID,
		plan.sourceGeneration,
	); err != nil {
		// The child transcript is already durable. Preserve the pending create
		// so the same operation can finish publication without copying history.
		return session.SessionRef{}, err
	}
	a.emitProjectTreeChanged()
	return forked.Child, nil
}

func (a *App) localizedForkTitleSuffix() string {
	switch a.desktopLocale.Load() {
	case desktopLocaleEn:
		return " · Fork"
	case desktopLocaleZhTW:
		return " · 分叉"
	default:
		return " · 分叉"
	}
}

// ForkSessionTarget forks one explicit durable target without staging it into
// the current tab. Legacy-only sources must first be adopted into canonical
// storage so turn boundaries remain verifiable.
func (a *App) ForkSessionTarget(selector SessionSelector, turnBoundary string) (session.SessionRef, error) {
	target, err := a.resolveSessionMutationTarget(selector)
	if err != nil {
		return session.SessionRef{}, err
	}
	if target.SessionRef.SessionID == "" {
		return session.SessionRef{}, newSessionOperationError("unsupported", "This historical session has no verifiable canonical turn boundaries.")
	}
	plan, err := a.planCanonicalFork(target.SessionRef, turnBoundary)
	if err != nil {
		return session.SessionRef{}, sessionOperationErrorForTarget(err, target.key(), "")
	}
	operation, err := a.beginForkOperation(forkOperation{
		Surface: "target", SourceHostID: target.SessionRef.HostID, SourceSessionID: target.SessionRef.SessionID,
		TurnID: plan.target.TurnID, BoundarySequence: plan.target.BoundarySequence,
	})
	if err != nil {
		return session.SessionRef{}, sessionOperationErrorForTarget(err, target.key(), "")
	}
	if operation.State == "completed" && strings.TrimSpace(operation.ChildSessionID) != "" {
		return session.SessionRef{HostID: localDesktopHostID, SessionID: operation.ChildSessionID}, nil
	}
	childID := "desktop-" + strings.TrimPrefix(operation.OperationID, "fork_")
	child, err := a.executeCanonicalFork(target.SessionRef, plan, operation.OperationID, childID)
	if err != nil {
		var unavailable *session.ForkUnavailableError
		if errors.As(err, &unavailable) {
			_ = a.discardForkOperation(operation.OperationID)
		}
		return session.SessionRef{}, sessionOperationErrorForTarget(err, target.key(), operation.OperationID)
	}
	if err := a.completeForkOperation(operation.OperationID, child.SessionID); err != nil {
		return session.SessionRef{}, sessionOperationErrorForTarget(err, target.key(), operation.OperationID)
	}
	return child, nil
}
