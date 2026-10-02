package main

import (
	"slices"
	"strings"

	"reasonix/internal/control"
)

func (t *WorkspaceTab) sessionRuntimeLookupKeys() []string {
	if t == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var keys []string
	add := func(raw string) {
		key := sessionRuntimeKey(raw)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	add(t.currentSessionIdentity())
	add(t.currentSessionPath())
	if id := strings.TrimSpace(t.SessionID); id != "" {
		add(sessionRoute(id))
		// A lease protects a resource, not an arbitrary future session. Only
		// retain the import alias while this exact imported identity is bound.
		if _, runtime, exclusive := exclusiveSessionBinding(t.Ctrl); exclusive && runtime != nil && runtime.Ref().SessionID == id {
			if source := runtime.Session().Manifest().Source; source != nil {
				add(source.Path)
			}
		}
	}
	return keys
}

func sessionRuntimeKeysOverlap(tab *WorkspaceTab, identity string) bool {
	want := sessionRuntimeKey(identity)
	if want == "" || tab == nil {
		return false
	}
	return slices.Contains(tab.sessionRuntimeLookupKeys(), want)
}

func (a *App) liveRuntimeTabMatchingLocked(exclude *WorkspaceTab, identity string) *WorkspaceTab {
	if a == nil {
		return nil
	}
	want := sessionRuntimeKey(identity)
	match := func(tab *WorkspaceTab) bool {
		if tab == nil || tab == exclude || tab.Ctrl == nil {
			return false
		}
		if want == "" {
			return false
		}
		if id, canonical := parseSessionRoute(identity); canonical {
			if lifecycle, ok := tab.Ctrl.(control.IdentityLifecycle); ok && lifecycle.UsesExclusiveSession() {
				ref, bound := lifecycle.SessionRef()
				return bound && ref.SessionID == id
			}
		}
		return slices.Contains(tab.sessionRuntimeLookupKeys(), want)
	}
	if want != "" {
		if rt := a.runtimeBySessionKey[want]; rt != nil && rt.Owner != exclude && a.runtimeOwnerLiveLocked(rt) && rt.Phase == sessionRuntimeReady && match(rt.Owner) {
			return rt.Owner
		}
		if tab := a.detachedSessions[want]; match(tab) {
			return tab
		}
	}
	for _, tab := range a.runtimeTabsLocked() {
		if match(tab) {
			return tab
		}
	}
	return nil
}

func (a *App) liveRuntimeTabMatchingTopicLocked(exclude *WorkspaceTab, scope, workspaceRoot, topicID string) *WorkspaceTab {
	topicID = strings.TrimSpace(topicID)
	if a == nil || topicID == "" {
		return nil
	}
	var found *WorkspaceTab
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || tab == exclude || tab.Ctrl == nil || !tabMatchesTopicTarget(tab, scope, workspaceRoot, topicID) {
			continue
		}
		if found != nil {
			return nil
		}
		found = tab
	}
	return found
}

func (a *App) registerDetachedRuntimeLocked(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	a.ensureDetachedSessionsLocked()
	for _, key := range tab.sessionRuntimeLookupKeys() {
		a.detachedSessions[key] = tab
		a.aliasSessionRuntimeKeyLocked(tab, key)
	}
}

func (a *App) unregisterDetachedRuntimeLocked(tab *WorkspaceTab) {
	if a.detachedSessions == nil || tab == nil {
		return
	}
	for key, candidate := range a.detachedSessions {
		if candidate == tab {
			delete(a.detachedSessions, key)
		}
	}
}

func (a *App) aliasSessionRuntimeKeyLocked(tab *WorkspaceTab, key string) {
	if tab == nil || key == "" {
		return
	}
	if existing := a.runtimeBySessionKey[key]; existing != nil && existing.Owner != tab && a.runtimeOwnerLiveLocked(existing) {
		return
	}
	rt := a.runtimeForTabLocked(tab)
	if rt == nil {
		a.newSessionRuntimeLocked(tab, key)
		return
	}
	a.runtimeBySessionKey[key] = rt
}

func runtimeAttachIdentity(source *WorkspaceTab, fallback string) string {
	if source != nil {
		if identity := source.currentSessionIdentity(); identity != "" {
			return identity
		}
	}
	return fallback
}

// App.mu, runtimeRebuildMu and turnStartMu guard this handoff. A running source
// keeps its runtime, sink and writer; only the target reservation is replaced.
func (a *App) commitCanonicalRuntimeTransitionLocked(tab *WorkspaceTab, transition sessionRuntimePathTransition, preserve bool) bool {
	if !preserve {
		return a.commitSessionRuntimePathLocked(transition)
	}
	if !a.sessionRuntimePathTransitionValidLocked(transition) || !a.detachRuntimeForReplacementLocked(tab) {
		return false
	}
	if a.runtimeBySessionKey[transition.targetKey] == transition.runtime {
		delete(a.runtimeBySessionKey, transition.targetKey)
	}
	return true
}

func fenceCanonicalNavigationSink(sink *tabEventSink) {
	if sink != nil {
		sink.setBinding("", nil)
		sink.clearContext()
	}
}
