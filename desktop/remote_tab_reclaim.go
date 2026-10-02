package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func takeoverViewLocallyOwned(view SessionTakeoverView) bool {
	return view.Mirrored || view.Holder == "external" || view.Holder == "other"
}

// reconcileRemoteTabReclaimOwnership keeps an ambiguous reclaim response from
// changing input authority. Only a successful, generation-fenced ownership
// probe may update the spectator pin.
func (a *App) reconcileRemoteTabReclaimOwnership(
	tabID string,
	client *http.Client,
	base, expectedPath string,
	stillCurrent func(*remoteTab) bool,
) {
	a.goRemoteTabSafe("reclaimOwnershipProbe", func() {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer probeCancel()
		view, err := takeoverOwnership(probeCtx, client, base, expectedPath)
		if err != nil {
			return
		}
		locallyOwned := takeoverViewLocallyOwned(view)
		a.remoteTabMu.Lock()
		current := a.remoteTabs[tabID]
		if !stillCurrent(current) || current.session.takenOver == locallyOwned {
			a.remoteTabMu.Unlock()
			return
		}
		current.session.takenOver = locallyOwned
		meta := remoteTabMetaLocked(current)
		a.remoteTabMu.Unlock()
		a.emitRemoteEvent("remote-tab:updated", meta)
	})
}

// remoteTabOwnershipState fences a session's return from a local writer back
// to Serve. Both fields share that lifetime: a committing reclaim sets them
// and the re-hydrated surface clears them.
//
// reclaimRevision rejects /status payloads reserved before the reclaim
// completed — they still carry the pre-reclaim takenOver=true and would
// re-pin the spectator banner after ownership returned. readyBarrierPending
// defers the re-hydration barrier while a turn is in flight, because firing
// it mid-turn bumps the frontend connection generation, orphans the
// optimistic submission, and leaves a zombie "processing" indicator beside
// the rendered reply.
type remoteTabOwnershipState struct {
	reclaimRevision     uint64
	readyBarrierPending bool
}

// remoteTabReclaimObservation is the tab state a reclaim fences against: it
// releases remoteTabMu for a long poll, then dereferences tab.
type remoteTabReclaimObservation struct {
	tab               *remoteTab
	gen               uint64
	runtimeRevision   uint64
	selectionRevision uint64
}

// observeRemoteTabForReclaim snapshots the tab a reclaim fences against. The
// tab can close or reconnect between the command-target read and this
// snapshot; either is a disconnected tab, not a nil or retired binding.
func (a *App) observeRemoteTabForReclaim(tabID string, client *http.Client) (remoteTabReclaimObservation, error) {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	tab := a.remoteTabs[tabID]
	if tab == nil || tab.client != client {
		return remoteTabReclaimObservation{}, fmt.Errorf("remote tab %q is not connected", tabID)
	}
	return remoteTabReclaimObservation{
		tab: tab, gen: tab.gen,
		runtimeRevision: tab.runtime.revision, selectionRevision: tab.selectionRevision,
	}, nil
}

// remoteSessionTakenOver reports whether a session-entry refusal means the
// session is owned by a local runtime on the serve host. The tab then
// attaches as a read-only spectator instead of dying with the 409. All three
// refusal shapes match: the explicit takeover wording (mirrored session), the
// plain lease wording ("in use by another Reasonix process" — the holder is a
// local window/CLI whose transcript the file-backed /history serves anyway,
// and whose lease /reclaim can take back), and the final-format writer
// wording ("session writer is owned by another runtime" — the identity's
// writer.lock lives with a local runtime).
func remoteSessionTakenOver(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "taken over by a local Reasonix") {
		return true
	}
	if strings.Contains(msg, "writer is owned by another runtime") {
		return true
	}
	return strings.Contains(msg, "in use by another Reasonix process")
}
