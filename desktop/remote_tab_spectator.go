package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"reasonix/internal/agent"
)

// markRemoteTabSpectatorIfLocalOwned reconciles ownership when a mid-view
// status/notice needs an authoritative probe, or when an older Serve omitted
// the synchronous ownership header used by attach and resume responses.
func (a *App) markRemoteTabSpectatorIfLocalOwned(ctx context.Context, tabID string, client *http.Client, base string, gen uint64) {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	path := ""
	selectionRevision, reclaimRevision := uint64(0), uint64(0)
	if tab != nil && tab.gen == gen {
		path = strings.TrimSpace(tab.routing.currentPath)
		selectionRevision = tab.selectionRevision
		reclaimRevision = tab.ownership.reclaimRevision
	}
	a.remoteTabMu.Unlock()
	if path == "" {
		return
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	view, probeErr := takeoverOwnership(probeCtx, client, base, path)
	probeCancel()
	// A transport failure says nothing about ownership. Preserve the current
	// spectator pin and let the next status/notice probe retry instead of
	// briefly reopening input against an ownership state we could not prove.
	if probeErr != nil {
		return
	}
	// Serve mounts both holders — "external" (mirrored local writer) and
	// "other" (lease without a registered mirror) — as read-only spectator
	// surfaces, so the banner and take-back button must appear for either.
	locallyOwned := takeoverViewLocallyOwned(view)
	a.remoteTabMu.Lock()
	current := a.remoteTabs[tabID]
	if current != tab || current == nil || current.gen != gen || current.client != client ||
		current.selectionRevision != selectionRevision ||
		agent.CanonicalSessionPath(current.routing.currentPath) != agent.CanonicalSessionPath(path) {
		a.remoteTabMu.Unlock()
		return
	}
	if !locallyOwned {
		// The probed session is not locally owned (e.g. a fresh /new or a
		// free session). Clear any stale spectator pin left over from the
		// previous session so the banner and read-only composer go away.
		current.session.takenOver = false
		a.remoteTabMu.Unlock()
		return
	}
	// A reclaim completed while this probe was in flight: its answer describes
	// the ownership that reclaim ended. Same fence in-flight status gets.
	if current.ownership.reclaimRevision != reclaimRevision {
		a.remoteTabMu.Unlock()
		return
	}
	current.session.takenOver = true
	a.remoteTabMu.Unlock()
	// No remote-tab:updated emit: racing hydration re-renders mid-fetch and
	// looks like a retry loop. The /status poll picks takenOver up instead.
	slog.Info("desktop: remote tab switched to spectator on local-owned session",
		"tab", tabID, "session", path, "holder", view.Holder)
}

// probeSpectatorAfterNotice re-probes spectator state when Serve broadcasts a
// takeover or reclaim notice for a session. The entry-time probe cannot see
// mid-view transitions, so without this the banner and the composer lock
// drift from the real ownership until the next session switch.
func (a *App) probeSpectatorAfterNotice(tabID string, gen uint64, client *http.Client, base, framePath string) {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	viewing := tab != nil && tab.gen == gen && tab.routing.currentPath != "" &&
		agent.CanonicalSessionPath(tab.routing.currentPath) == agent.CanonicalSessionPath(framePath)
	a.remoteTabMu.Unlock()
	if !viewing {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a.markRemoteTabSpectatorIfLocalOwned(ctx, tabID, client, base, gen)
}

// clearRemoteTabSpectator drops the read-only spectator pin when the route it
// was pinned to lost the publication fence (or after reclaim lands the
// foreground on the session).
func (a *App) clearRemoteTabSpectator(tabID string, gen uint64) {
	a.remoteTabMu.Lock()
	current := a.remoteTabs[tabID]
	// gen 0 means "any generation" — used by failure cleanups where the
	// caller doesn't track the exact generation.
	if current == nil || (gen != 0 && current.gen != gen) {
		a.remoteTabMu.Unlock()
		return
	}
	if !current.session.takenOver {
		a.remoteTabMu.Unlock()
		return
	}
	current.session.takenOver = false
	a.remoteTabMu.Unlock()
	// No emit: let the /status poll propagate the change.
}
