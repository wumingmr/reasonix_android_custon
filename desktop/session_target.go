package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/session"
	"reasonix/internal/sessioncatalog"
)

type SessionOperationMode int

const (
	OperationPersistent SessionOperationMode = iota
	OperationRuntime
)

const (
	sessionOperationTargetNotFound  = "target_not_found"
	sessionOperationNoMessages      = "no_messages"
	sessionOperationRuntimeNotOpen  = "runtime_not_open"
	sessionOperationRuntimeNotReady = "runtime_not_ready"
	sessionOperationTitleConflict   = "title_conflict"
	sessionOperationBusy            = "operation_busy"
	sessionOperationFailed          = "operation_failed"
	sessionOperationDamaged         = "session_damaged"
)

// SessionOperationError is stable at the host boundary: the code is intended
// for frontend localization while the message remains safe for older clients.
type SessionOperationError struct {
	ReadReason  string `json:"readReason,omitempty"`
	Code        string
	Message     string
	TargetKey   string
	OperationID string
	Retryable   bool
}

func (e *SessionOperationError) Error() string {
	if e == nil {
		return ""
	}
	return "session_operation:" + e.Code + ":" + e.Message
}

func newSessionOperationError(code, message string) error {
	retryable := code == sessionOperationRuntimeNotReady || code == sessionOperationTitleConflict ||
		code == sessionOperationBusy || code == "target_changed" || code == "stale_cursor"
	return &SessionOperationError{Code: code, Message: message, Retryable: retryable}
}

// RPCErrorData exposes product-safe structured details to the generic host
// transport without making hostrpc depend on Desktop application types.
func (e *SessionOperationError) RPCErrorData() map[string]any {
	if e == nil {
		return nil
	}
	data := map[string]any{"sessionCode": e.Code, "retryable": e.Retryable}
	if e.ReadReason != "" {
		data["readReason"] = e.ReadReason
	}
	if e.TargetKey != "" {
		data["targetKey"] = e.TargetKey
	}
	if e.OperationID != "" {
		data["operationId"] = e.OperationID
	}
	return data
}

// SessionSelector is the stable target address accepted by session-level
// operations. Ref contains the canonical host-qualified session ID; TopicID is
// only the lowest-priority legacy/topic-only compatibility lookup.
// Higher-priority fields never fall back when invalid.
type SessionSelector struct {
	Source      *SessionSourceRef   `json:"source,omitempty"`
	Ref         *session.SessionRef `json:"ref,omitempty"`
	SessionPath string              `json:"sessionPath,omitempty"`
	TopicID     string              `json:"topicId,omitempty"`
}

type SessionSourceRef struct {
	HostID    string `json:"hostId"`
	SourceKey string `json:"sourceKey,omitempty"`
	Path      string `json:"path"`
	HeadID    string `json:"headId,omitempty"`
}

// SessionTarget resolves durable identity independently from runtime state.
// Controller is optional and is never used to decide whether the session
// exists.
type SessionTarget struct {
	Source              *SessionSourceRef
	TopicID             string
	SessionRef          session.SessionRef
	SessionPath         string
	Scope               string
	WorkspaceRoot       string
	IsOpen              bool
	Ready               bool
	TabID               string
	Controller          *control.Controller
	WorkspaceID         string
	LifecycleGeneration uint64
	Lifecycle           string
	SharedTopic         bool
}

type sessionTargetSelector = SessionSelector

func (target SessionTarget) key() string {
	if strings.TrimSpace(target.SessionRef.SessionID) != "" {
		return "ref:" + target.SessionRef.HostID + ":" + target.SessionRef.SessionID
	}
	if target.Source != nil {
		return "source:" + target.Source.HostID + ":" + target.Source.SourceKey
	}
	if path := strings.TrimSpace(target.SessionPath); path != "" {
		return "path:" + sessionRuntimeKey(path)
	}
	return "topic:" + strings.TrimSpace(target.TopicID)
}

func (target SessionTarget) RequireRuntime() (*control.Controller, error) {
	if !target.IsOpen || target.Controller == nil {
		return nil, newSessionOperationError(sessionOperationRuntimeNotOpen, "Open this session before using this action.")
	}
	if !target.Ready {
		return nil, newSessionOperationError(sessionOperationRuntimeNotReady, "This session is still loading. Try again shortly.")
	}
	return target.Controller, nil
}

func (a *App) resolveSessionTarget(selector sessionTargetSelector) (SessionTarget, error) {
	return a.resolveSessionTargetWithArchived(selector, false)
}

func (a *App) resolveSessionTargetWithArchived(selector sessionTargetSelector, allowArchived bool) (SessionTarget, error) {
	if selector.Ref != nil {
		if strings.TrimSpace(selector.Ref.SessionID) == "" {
			return SessionTarget{}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
		}
		if hostID := strings.TrimSpace(selector.Ref.HostID); hostID != "" && hostID != localDesktopHostID {
			return SessionTarget{}, newSessionOperationError("unsupported", "This remote session operation is not available from the local session service.")
		}
		return a.resolveCanonicalSessionTargetState(*selector.Ref, strings.TrimSpace(selector.TopicID), allowArchived)
	}
	if selector.Source != nil {
		return a.resolveSourceSessionTarget(selector, allowArchived)
	}
	if path := strings.TrimSpace(selector.SessionPath); path != "" {
		if source, err := parseSessionSourceRoute(path); err != nil {
			return SessionTarget{}, err
		} else if source != nil {
			return a.resolveSessionTargetWithArchived(SessionSelector{Source: source, TopicID: selector.TopicID}, allowArchived)
		}
		if ref, ok := sessionRefForRoute(a.desktopSessionService(""), path); ok {
			return a.resolveCanonicalSessionTargetState(ref, strings.TrimSpace(selector.TopicID), allowArchived)
		}
		return a.resolveLegacySessionTarget(path, strings.TrimSpace(selector.TopicID), allowArchived)
	}
	topicID := strings.TrimSpace(selector.TopicID)
	if topicID == "" {
		return SessionTarget{}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	ref, canonical, err := a.canonicalSessionRefForTopic(topicID)
	if err != nil {
		return SessionTarget{}, err
	}
	scope, root, ok := a.findTopicLocation(topicID)
	legacyPaths := a.legacySessionPathsForTopic(scope, root, topicID)
	if canonical {
		if len(legacyPaths) != 0 {
			return SessionTarget{}, newSessionOperationError("ambiguous_target", "Select a specific session before using this action.")
		}
		return a.resolveCanonicalSessionTargetState(ref, topicID, allowArchived)
	}
	if len(legacyPaths) > 1 {
		return SessionTarget{}, newSessionOperationError("ambiguous_target", "Select a specific session before using this action.")
	}
	if len(legacyPaths) == 1 {
		if rows := expandSessionSourceRows(ProjectNode{SessionPath: legacyPaths[0]}); len(rows) > 1 {
			return SessionTarget{}, newSessionOperationError("ambiguous_target", "Select a specific historical head before using this action.")
		}
		target, resolveErr := a.resolveLegacySessionTarget(legacyPaths[0], topicID, allowArchived)
		if resolveErr == nil {
			target.Scope, target.WorkspaceRoot = scope, root
		}
		return target, resolveErr
	}
	if runtime := a.runtimeSessionTarget(topicID, session.SessionRef{}, ""); runtime.Controller != nil {
		if runtimeRef, bound := runtime.Controller.SessionRef(); bound {
			target, resolveErr := a.resolveCanonicalSessionTargetState(runtimeRef, topicID, allowArchived)
			if resolveErr == nil {
				return target, nil
			}
		}
	}
	if !ok {
		return SessionTarget{}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	if runtime := a.runtimeSessionTarget(topicID, session.SessionRef{}, ""); runtime.IsOpen {
		runtime.Scope, runtime.WorkspaceRoot = scope, root
		return runtime, nil
	}
	// A newly created topic can exist before its first user turn has allocated
	// physical session storage. It is a valid empty target, not a missing one.
	return SessionTarget{TopicID: topicID, Scope: scope, WorkspaceRoot: root}, nil
}

func (a *App) legacySessionPathsForTopic(scope, workspaceRoot, topicID string) []string {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return nil
	}
	mapped := map[string]bool{}
	if state, err := a.workspaceRegistry().Load(a.bootContext()); err == nil {
		for _, mapping := range state.SourceMappings {
			if path := strings.TrimSpace(mapping.Path); path != "" {
				mapped[sessionRuntimeKey(path)] = true
			}
		}
	}
	paths := map[string]string{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, canonical := parseSessionRoute(path); canonical {
			return
		}
		key := sessionRuntimeKey(path)
		if key == "" || mapped[key] || agent.IsCleanupPending(path) {
			return
		}
		paths[key] = path
	}
	if scope != "" {
		if catalog := a.sessionCatalog.Load(); catalog != nil {
			topic, found, err := catalog.GetTopic(a.bootContext(), sessioncatalog.TopicKey{
				Scope: scope, WorkspaceRoot: workspaceRoot, TopicID: topicID,
			})
			if err == nil && found {
				for _, record := range topic.Sessions {
					add(record.Path)
				}
			}
		}
	}
	for _, dir := range a.knownSessionDirs() {
		for _, match := range topicSessionMatches(dir, topicID) {
			add(match.path)
		}
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func (a *App) canonicalSessionRefForTopic(topicID string) (session.SessionRef, bool, error) {
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return session.SessionRef{}, false, err
	}
	var found session.SessionRef
	for _, workspace := range state.Workspaces {
		for _, id := range workspace.SessionIDs {
			presentation := state.Presentation[id]
			if presentation.TopicID == topicID || "canonical-"+id == topicID || id == topicID || sessionRoute(id) == topicID {
				if found.SessionID != "" && found.SessionID != id {
					return session.SessionRef{}, false, newSessionOperationError("ambiguous_target", "Select a specific session before renaming it.")
				}
				found = session.SessionRef{HostID: localDesktopHostID, SessionID: id}
			}
		}
	}
	return found, found.SessionID != "", nil
}

func (a *App) resolveCanonicalSessionTarget(ref session.SessionRef, topicID string) (SessionTarget, error) {
	return a.resolveCanonicalSessionTargetState(ref, topicID, false)
}

func (a *App) resolveCanonicalSessionTargetState(ref session.SessionRef, topicID string, allowArchived bool) (SessionTarget, error) {
	if err := validateLocalSessionRef(ref); err != nil {
		return SessionTarget{}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	if _, err := a.desktopSessionService("").Query().Stat(a.bootContext(), ref); err != nil {
		return SessionTarget{}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	target := a.runtimeSessionTarget(topicID, ref, sessionRoute(ref.SessionID))
	target.SessionRef = ref
	target.SessionPath = sessionRoute(ref.SessionID)
	if target.TopicID == "" {
		target.TopicID = topicID
	}
	snapshot, loadErr := a.workspaceRegistry().VerifySnapshot(a.bootContext())
	if loadErr != nil {
		return SessionTarget{}, loadErr
	}
	metadata := snapshot.Session(ref.SessionID)
	status := metadata.State
	if !metadata.Registered || status.Lifecycle == workspacestate.Deleted {
		return SessionTarget{}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	if status.Lifecycle != workspacestate.Active && !allowArchived {
		return SessionTarget{}, newSessionOperationError("archived", "Restore this session before renaming it.")
	}
	target.LifecycleGeneration = status.Generation
	target.Lifecycle = status.Lifecycle
	// Presentation belongs to this exact session, never a caller's stale topic.
	target.TopicID, target.SharedTopic = metadata.Presentation.TopicID, metadata.SharedTopic
	if metadata.OwnershipConflict {
		return SessionTarget{}, errSessionWorkspaceConflict
	}
	if workspace := metadata.Workspace; workspace.ID != "" {
		target.WorkspaceRoot, target.WorkspaceID = workspace.Root, workspace.ID
		target.Scope = canonicalWorkspaceScope(workspace)
		if target.Scope == "global" {
			target.WorkspaceRoot = ""
		}
	}
	if target.TopicID == "" {
		target.TopicID = "canonical-" + ref.SessionID
	}
	return target, nil
}

func (a *App) resolveLegacySessionTarget(path, topicID string, allowArchived bool) (SessionTarget, error) {
	dir, validated, err := a.sessionDirForPath(path)
	if err != nil {
		return SessionTarget{}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	if _, _, err := validateSessionPath(dir, validated); err != nil {
		return SessionTarget{}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	if ref, adopted, adoptionErr := a.legacyCanonicalRef(a.bootContext(), validated); adoptionErr != nil {
		return SessionTarget{}, newSessionOperationError("target_changed", "The session location or identity changed. Reload it and try again.")
	} else if adopted {
		return a.resolveCanonicalSessionTargetState(ref, topicID, allowArchived)
	}
	target := a.runtimeSessionTarget(topicID, session.SessionRef{}, validated)
	target.SessionPath = validated
	target.TopicID = topicID
	if meta, ok, err := agent.LoadBranchMeta(validated); err == nil && ok {
		target.TopicID, target.Scope, target.WorkspaceRoot = meta.TopicID, meta.Scope, meta.WorkspaceRoot
	}
	return target, nil
}

func (a *App) runtimeSessionTarget(topicID string, ref session.SessionRef, path string) SessionTarget {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil {
			continue
		}
		ctrl, _ := tab.Ctrl.(*control.Controller)
		matches := false
		switch {
		case ref.SessionID != "":
			if ctrl != nil {
				bound, ok := ctrl.SessionRef()
				matches = ok && bound == ref
			}
		case path != "":
			matches = sessionRuntimeKey(tab.currentSessionPath()) == sessionRuntimeKey(path)
		default:
			matches = topicID != "" && (tab.TopicID == topicID || tab.SessionID == topicID || sessionRoute(tab.SessionID) == topicID)
		}
		if !matches {
			continue
		}
		resolvedPath := path
		if resolvedPath == "" {
			resolvedPath = tab.currentSessionPath()
		}
		resolvedRef := ref
		if resolvedRef.SessionID == "" && ctrl != nil {
			if bound, ok := ctrl.SessionRef(); ok {
				resolvedRef = bound
			}
		}
		return SessionTarget{
			TopicID: topicID, SessionRef: resolvedRef, SessionPath: resolvedPath,
			Scope: tab.Scope, WorkspaceRoot: tab.WorkspaceRoot,
			IsOpen: true, Ready: tab.Ready, TabID: tab.ID, Controller: ctrl,
		}
	}
	return SessionTarget{TopicID: topicID, SessionRef: ref, SessionPath: path}
}

// sessionTargetRuntimeRebound detects reuse of the same controller for another
// durable identity. Merely switching or closing the tab is not a conflict for
// a persistent operation; storage CAS remains authoritative in that case.
func (a *App) sessionTargetRuntimeRebound(target SessionTarget) bool {
	if !target.IsOpen || target.Controller == nil || target.TabID == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	tab := a.tabs[target.TabID]
	if tab == nil || tab.Ctrl != target.Controller {
		return false
	}
	if target.SessionRef.SessionID != "" {
		ref, ok := target.Controller.SessionRef()
		return !ok || ref != target.SessionRef
	}
	return sessionRuntimeKey(tab.currentSessionPath()) != sessionRuntimeKey(target.SessionPath)
}

func sessionOperationConflict(err error) error {
	if errors.Is(err, workspacestate.ErrSessionNotFound) {
		return newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists or has been archived.")
	}
	if errors.Is(err, workspacestate.ErrMutationConflict) {
		return newSessionOperationError(sessionOperationTitleConflict, "The session changed while AI rename was running. Try again.")
	}
	if errors.Is(err, session.ErrSessionTitleChanged) {
		return newSessionOperationError(sessionOperationTitleConflict, "The session title changed while AI rename was running. Try again.")
	}
	return fmt.Errorf("AI rename session: %w", err)
}
