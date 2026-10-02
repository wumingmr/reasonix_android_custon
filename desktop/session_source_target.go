package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"reasonix/internal/session"
	"strings"
)

// Topic-only navigation remains a compatibility entrypoint. It may create an
// empty surface, but must never choose one of several durable sessions.
func (a *App) resolveTopicOpenPath(scope, root, topicID string) (string, error) {
	if strings.TrimSpace(topicID) != "" {
		target, err := a.resolveSessionTarget(SessionSelector{TopicID: topicID})
		if err == nil && target.SessionPath != "" {
			return target.SessionPath, nil
		}
		var operationError *SessionOperationError
		if err != nil && (!errors.As(err, &operationError) || operationError.Code != sessionOperationTargetNotFound) {
			return "", err
		}
	}
	path, _ := a.findTopicSessionForTarget(scope, root, topicID)
	return path, nil
}

// Explicit management operations retain their journaled ownership transition.
// Navigation and runtime rebuilds must use the read-only resolver instead.
func (a *App) resolveSessionMutationTarget(selector SessionSelector) (SessionTarget, error) {
	target, err := a.resolveSessionTarget(selector)
	if err != nil || target.Source == nil {
		return target, err
	}
	view, err := a.PrepareSession(SessionSelector{Source: target.Source, TopicID: target.TopicID})
	if err != nil {
		return SessionTarget{}, err
	}
	c := &a.historicalImports
	c.mu.Lock()
	call := c.operations[view.OperationID]
	c.mu.Unlock()
	if call == nil {
		return SessionTarget{}, newSessionOperationError("target_changed", "The source preparation task is unavailable.")
	}
	result, err := waitHistoricalImport(call)
	if err != nil {
		return SessionTarget{}, err
	}
	return a.resolveCanonicalSessionTarget(result.Session, target.TopicID)
}

func parseSessionSourceRoute(route string) (*SessionSourceRef, error) {
	const prefix = "session-source:"
	if !strings.HasPrefix(route, prefix) {
		return nil, nil
	}
	text, err := url.PathUnescape(strings.TrimPrefix(route, prefix))
	if err != nil {
		return nil, err
	}
	var source SessionSourceRef
	if err := json.Unmarshal([]byte(text), &source); err != nil {
		return nil, err
	}
	return &source, nil
}

func nativeSessionSourceRoute(source *SessionSourceRef) string {
	data, _ := json.Marshal(source)
	return "session-source:" + url.PathEscape(string(data))
}

// Session title projection is addressed by the same durable ID as its write.
// A topic may gain another branch while an asynchronous title is generated.
func (a *App) updateCanonicalSessionTitle(ref session.SessionRef, title, source string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.SessionID == ref.SessionID {
			tab.TopicTitle, tab.topicTitleSource = title, source
		}
	}
	a.saveTabsLocked()
}
