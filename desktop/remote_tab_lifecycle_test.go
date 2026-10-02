package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// errEnsureHealing stands in for the transient EnsureServer failures observed
// while the SSH layer is still re-establishing a dropped tunnel.
var errEnsureHealing = errors.New("tunnel healing")

func TestRemoteTabSnapshotReplaysAndClearsPendingPrompt(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	a.remoteTabMu.Lock()
	gen := a.remoteTabs[meta.ID].gen
	a.remoteTabMu.Unlock()
	a.cacheRemotePendingEvent(meta.ID, gen, "approval_request", json.RawMessage(`{"kind":"approval_request","approval":{"id":"approval-1","tool":"bash"}}`))
	snap, err := a.RemoteTabSnapshot(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.PendingEvents) != 1 || !strings.Contains(string(snap.PendingEvents[0]), "approval-1") {
		t.Fatalf("pending replay = %s", snap.PendingEvents)
	}
	if err := a.ApproveRemoteTab(meta.ID, "approval-1", "deny"); err != nil {
		t.Fatal(err)
	}
	snap, err = a.RemoteTabSnapshot(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.PendingEvents) != 0 {
		t.Fatalf("resolved prompt was still replayed: %s", snap.PendingEvents)
	}

	form := json.RawMessage(`{"kind":"extension_surface","extension":{"pluginId":"remote-plugin","surfaceId":"setup","kind":"form","form":{"title":"Remote setup","fields":[{"key":"region","label":"Region","kind":"input"}]}}}`)
	if !a.cacheRemotePendingExtensionForm(meta.ID, gen, form) {
		t.Fatal("actionable extension form was not retained")
	}
	snap, err = a.RemoteTabSnapshot(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.PendingEvents) != 1 || !strings.Contains(string(snap.PendingEvents[0]), "remote-plugin") {
		t.Fatalf("pending extension form replay = %s", snap.PendingEvents)
	}
	fs.mu.Lock()
	fs.failNext = "form rejected"
	fs.mu.Unlock()
	if err := a.SubmitRemoteTabExtensionForm(meta.ID, "remote-plugin", "setup", map[string]any{"region": "us-west"}); err == nil {
		t.Fatal("failed extension form submission succeeded")
	}
	snap, err = a.RemoteTabSnapshot(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.PendingEvents) != 1 {
		t.Fatalf("failed extension form submission cleared replay: %s", snap.PendingEvents)
	}
	if err := a.SubmitRemoteTabExtensionForm(meta.ID, "remote-plugin", "setup", map[string]any{"region": "us-west"}); err != nil {
		t.Fatal(err)
	}
	snap, err = a.RemoteTabSnapshot(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.PendingEvents) != 0 {
		t.Fatalf("submitted extension form was still replayed: %s", snap.PendingEvents)
	}
}

func TestRemoteTabSnapshotRehydratesAndDropsPriorSessionPromptOnStatusAdoption(t *testing.T) {
	const firstPath = "/sessions/first.jsonl"
	const nextPath = "/sessions/next.jsonl"
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "first", Path: firstPath, Current: true}})
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	log := &eventLog{}
	a := &App{remoteRuntime: kernel, remoteEventHook: log.add}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{SessionName: "first", SessionPath: firstPath})
	a.remoteTabMu.Lock()
	gen := a.remoteTabs[meta.ID].gen
	a.remoteTabMu.Unlock()
	a.cacheRemotePendingEvent(meta.ID, gen, "approval_request", json.RawMessage(`{"kind":"approval_request","approval":{"id":"old-approval"}}`))
	readyBefore := log.count("remote-tab:" + meta.ID + ":state ")
	fs.mu.Lock()
	fs.statusPayload = `{"sessionName":"next","sessionPath":"` + nextPath + `","pendingPrompt":false}`
	fs.mu.Unlock()
	snap, err := a.RemoteTabSnapshot(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.PendingEvents) != 0 {
		t.Fatalf("new session snapshot replayed prior prompt: %s", snap.PendingEvents)
	}
	a.remoteTabMu.Lock()
	path := a.remoteTabs[meta.ID].routing.currentPath
	a.remoteTabMu.Unlock()
	if path != nextPath || log.count("remote-tab:"+meta.ID+":state ") != readyBefore+1 {
		t.Fatalf("status adoption path/ready barrier = %q/%v", path, log.recorded())
	}
}

func TestRemoteTabDoesNotPublishReadyWithoutEventStream(t *testing.T) {
	previousDelays := remoteTabReattachDelays
	remoteTabReattachDelays = nil
	t.Cleanup(func() { remoteTabReattachDelays = previousDelays })

	fs := newFakeServe(t, "s3cret", nil)
	fs.mu.Lock()
	fs.eventsStatus = http.StatusServiceUnavailable
	fs.mu.Unlock()
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	// A refusing stream no longer parks the tab in error — it retries through
	// the reattach loop and only then parks in serve_down. Ready must never
	// publish without a live stream either way.
	waitForTabState(t, a, meta.ID, "serve_down")
	time.Sleep(50 * time.Millisecond)
	a.remoteTabMu.Lock()
	state := a.remoteTabs[meta.ID].state
	a.remoteTabMu.Unlock()
	if state == "ready" {
		t.Fatal("tab published ready after /events failed")
	}
}

func TestRemoteTabDoesNotPublishReadyWhenEventStreamClosesDuringAttach(t *testing.T) {
	previousDelays := remoteTabReattachDelays
	remoteTabReattachDelays = nil
	t.Cleanup(func() { remoteTabReattachDelays = previousDelays })

	fs := newFakeServe(t, "s3cret", nil)
	fs.mu.Lock()
	fs.eventsCloseEarly = true
	fs.enterDelay = 100 * time.Millisecond
	fs.mu.Unlock()
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	log := &eventLog{}
	a := &App{remoteRuntime: kernel, remoteEventHook: log.add}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "serve_down")
	for _, event := range log.recorded() {
		if strings.HasPrefix(event, "remote-tab:"+meta.ID+":state ") && strings.Contains(event, `"state":"ready"`) {
			t.Fatalf("closed event stream published ready: %v", log.recorded())
		}
	}
}

func TestRemoteTabReviveAppliesRequestedNamedSession(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "saved", Path: "/saved.jsonl", Title: "Saved"}})
	feed := make(chan string, 1)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	log := &eventLog{}
	a := &App{remoteRuntime: kernel, remoteEventHook: log.add}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	if tab.cancel != nil {
		tab.cancel()
	}
	tab.gen++
	tab.cancel, tab.client, tab.base, tab.token = nil, nil, "", ""
	tab.state = "disconnected"
	tab.session = remoteTabSessionState{newSession: true}
	a.remoteTabMu.Unlock()
	fs.mu.Lock()
	fs.eventFeed = feed
	fs.resumeStarted = make(chan string, 1)
	fs.resumeRelease = make(chan struct{})
	started, release := fs.resumeStarted, fs.resumeRelease
	fs.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	if _, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{SessionName: "saved"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("revived resume request did not start")
	}
	feed <- `{"kind":"notice","text":"revived output","sessionPath":"/saved.jsonl"}`
	deadline := time.Now().Add(time.Second)
	for log.count("remote-tab:"+meta.ID+":event") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("revived target frame was dropped while /resume was pending: %v", log.recorded())
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	waitForTabState(t, a, meta.ID, "ready")
	_, resumed, _ := fs.snapshot()
	if resumed != "/saved.jsonl" {
		t.Fatalf("revived shell resumed %q, want the selected session", resumed)
	}
}

func TestRemoteTabServeDownCanRetry(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "error", Error: "temporary"}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "serve_down")
	kernel.ensureView = RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}
	if _, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true}); err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")
}

func TestRemoteTabServeDownNewSessionClearsPendingBeforeDelayedMarker(t *testing.T) {
	const oldPath = "/sessions/old.jsonl"
	const freshPath = "/sessions/fresh.jsonl"
	feed := make(chan string, 1)
	fs := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	log := &eventLog{}
	a := &App{remoteRuntime: kernel, remoteEventHook: log.add}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})

	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	if tab.cancel != nil {
		tab.cancel()
	}
	tab.gen++
	tab.cancel, tab.client, tab.base, tab.token = nil, nil, "", ""
	tab.state = "serve_down"
	tab.session = remoteTabSessionState{name: "old", path: oldPath}
	tab.routing.currentPath = oldPath
	tab.pendingEvents = map[string]json.RawMessage{
		"approval_request:old": json.RawMessage(`{"kind":"approval_request","approval":{"id":"old"}}`),
	}
	tab.runtime = remoteTabRuntimeState{pendingPrompt: true, cancellable: true}
	a.remoteTabMu.Unlock()
	fs.mu.Lock()
	fs.newSessionPath = freshPath
	fs.eventFeed = feed
	fs.mu.Unlock()

	if _, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true}); err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")
	a.remoteTabMu.Lock()
	path, pending, prompt := tab.routing.currentPath, len(tab.pendingEvents), tab.runtime.pendingPrompt
	a.remoteTabMu.Unlock()
	if path != freshPath || pending != 0 || prompt {
		t.Fatalf("fresh attach route/pending/prompt = %q/%d/%v, want %q/0/false", path, pending, prompt, freshPath)
	}

	eventPrefix := "remote-tab:" + meta.ID + ":event"
	before := log.count(eventPrefix)
	feed <- `{"kind":"session_changed","sessionPath":"/sessions/fresh.jsonl","sessionCurrent":true,"sessionReset":true}`
	waitForRemoteEventCount(t, log, eventPrefix, before+1)
	a.remoteTabMu.Lock()
	pending, prompt = len(tab.pendingEvents), tab.runtime.pendingPrompt
	a.remoteTabMu.Unlock()
	if pending != 0 || prompt {
		t.Fatalf("delayed reset marker restored stale prompt: pending=%d prompt=%v", pending, prompt)
	}
}

func TestRemoteTabFocusOnlyAttachPreservesCurrentServeSession(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "current", Path: "/current.jsonl", Title: "Current", Current: true}})
	kernel := &fakeRemoteKernel{statuses: []RemoteConnectionStatusView{{HostID: "box", State: "connected"}}, ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret"}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")
	newCalled, resumed, _ := fs.snapshot()
	if newCalled != 0 || resumed != "" {
		t.Fatalf("focus-only attach changed Serve session: new=%d resume=%q", newCalled, resumed)
	}
}

func TestRemoteSavedSessionLookupCannotReviveDisconnectedGeneration(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "saved", Path: "/saved.jsonl", Title: "Saved"}})
	fs.sessionsStarted = make(chan struct{}, 1)
	fs.sessionsRelease = make(chan struct{})
	kernel := &fakeRemoteKernel{statuses: []RemoteConnectionStatusView{{HostID: "box", State: "connected"}}, ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret"}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})

	done := make(chan struct{})
	go func() { a.resumeRemoteTabSession(meta.ID, "saved"); close(done) }()
	select {
	case <-fs.sessionsStarted:
	case <-time.After(time.Second):
		t.Fatal("saved-session lookup did not start")
	}
	a.suspendRemoteTabPumps("box", "reconnecting", "")
	close(fs.sessionsRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("saved-session lookup did not finish")
	}
	a.remoteTabMu.Lock()
	state := a.remoteTabs[meta.ID].state
	a.remoteTabMu.Unlock()
	if state != "reconnecting" {
		t.Fatalf("stale session lookup changed state to %q", state)
	}
}

func TestRemoteTabReplacementServeReentersLearnedSessionBeforeReady(t *testing.T) {
	oldServe := newFakeServe(t, "s3cret", nil)
	newServe := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "generated", Path: "/generated.jsonl", Title: "Generated"}})
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: oldServe.server.URL, InstanceID: "serve-old"}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	oldServe.mu.Lock()
	oldServe.statusPayload = `{"running":false,"sessionName":"generated"}`
	oldServe.mu.Unlock()
	if _, err := a.RemoteTabStatus(meta.ID); err != nil {
		t.Fatal(err)
	}

	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	if tab.cancel != nil {
		tab.cancel()
	}
	tab.gen++
	tab.cancel, tab.client, tab.base, tab.token = nil, nil, "", ""
	tab.state = "reconnecting"
	// Restored tabs have no handshake metadata; stale capabilities from an
	// earlier service must also be replaced, not merged into the new binding.
	tab.capabilities = map[string]bool{"retired-capability": true}
	a.remoteTabMu.Unlock()
	kernel.ensureView = RemoteServerView{HostID: "box", State: "ready", LocalURL: newServe.server.URL, InstanceID: "serve-new"}

	if !a.reattachRemoteTabOnce(meta.ID) {
		t.Fatal("replacement serve reattach failed")
	}
	_, resumed, _ := newServe.snapshot()
	if resumed != "/generated.jsonl" {
		t.Fatalf("replacement serve resumed %q, want /generated.jsonl", resumed)
	}
	a.remoteTabMu.Lock()
	state, instanceID := a.remoteTabs[meta.ID].state, a.remoteTabs[meta.ID].session.instanceID
	a.remoteTabMu.Unlock()
	if state != "ready" || instanceID != "serve-new" {
		t.Fatalf("reattached state/instance = %q/%q", state, instanceID)
	}
	if err := a.requireRemoteExecutionProtocol(meta.ID); err != nil {
		t.Fatalf("reconnected service lost its execution capabilities: %v", err)
	}
	a.remoteTabMu.Lock()
	staleCapability := a.remoteTabs[meta.ID].capabilities["retired-capability"]
	a.remoteTabMu.Unlock()
	if staleCapability {
		t.Fatal("reconnect retained a capability absent from the new handshake")
	}
}

func TestRemoteTabServeDownRetryPreservesNamedSession(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "saved", Path: "/saved.jsonl", Title: "Saved"}})
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{SessionName: "saved"})
	a.parkRemoteTabsForServer("box", "~/app", "serve_down", "stopped")
	if _, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")
	_, resumed, _ := fs.snapshot()
	if resumed != "/saved.jsonl" {
		t.Fatalf("retry resumed %q, want the parked named session", resumed)
	}
}

func TestRemoteSnapshotRejectsChangedGeneration(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	fs.mu.Lock()
	fs.historyStarted = make(chan struct{}, 1)
	fs.historyRelease = make(chan struct{})
	started, release := fs.historyStarted, fs.historyRelease
	fs.mu.Unlock()
	errCh := make(chan error, 1)
	go func() {
		_, err := a.RemoteTabSnapshot(meta.ID)
		errCh <- err
	}()
	<-started
	a.suspendRemoteTabPumps("box", "reconnecting", "")
	close(release)
	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "changed while loading snapshot") {
		t.Fatalf("snapshot error = %v, want generation fence", err)
	}
}

func TestRemoteStopAndCloseCancelsBeforeRemovingTab(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	fs.mu.Lock()
	fs.statusPayload = `{"running":true,"pendingPrompt":false,"backgroundJobs":1,"cancellable":true,"jobs":[{"id":"job-remote","kind":"task","label":"verify","status":"running","startedAt":1}]}`
	fs.statusAfterCancel = `{"running":false,"pendingPrompt":false,"backgroundJobs":0,"cancellable":false}`
	fs.mu.Unlock()
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	work := a.ActiveWorkForTab(meta.ID)
	if !work.Running || !work.Cancellable {
		t.Fatalf("remote active work = %+v", work)
	}
	// The one-surface policy refuses to remove the sole visible surface. What
	// "stop and close" promises regardless is the stop, so that is what this
	// pins: both cancels land, and the tab stays because the close was refused.
	err := a.CloseTabWithPolicy(meta.ID, "stop_and_close")
	if err == nil || !strings.Contains(err.Error(), "cannot close the last tab") {
		t.Fatalf("stop-and-close on the sole surface = %v, want the last-surface refusal", err)
	}
	if !slices.ContainsFunc(fs.recorded(), func(call string) bool { return strings.HasPrefix(call, "POST /cancel") }) {
		t.Fatalf("stop-and-close did not cancel remote work: %v", fs.recorded())
	}
	if !slices.Contains(fs.recorded(), `POST /jobs/cancel {"ids":["job-remote"]}`) {
		t.Fatalf("stop-and-close did not cancel remote background jobs: %v", fs.recorded())
	}
	a.remoteTabMu.Lock()
	_, present := a.remoteTabs[meta.ID]
	a.remoteTabMu.Unlock()
	if !present {
		t.Fatal("a refused stop-and-close must leave the tab registered")
	}
}

// The observed tunnel-drop failure: the stream dies mid-turn and the first
// EnsureServer calls race the SSH layer's own recovery. The reattach loop
// must keep retrying across that window instead of parking a healthy tab in
// serve_down after half a second.
func TestRemoteTabReattachRetriesThroughTransientEnsureServerFailure(t *testing.T) {
	serve := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: serve.server.URL, InstanceID: "serve-1"}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	// The transient failures start after the tab is open: they stand in for
	// the tunnel dropping mid-session, not for a broken bootstrap.
	kernel.ensureErrs = []error{errEnsureHealing, errEnsureHealing}

	previousDelays := remoteTabReattachDelays
	remoteTabReattachDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { remoteTabReattachDelays = previousDelays })

	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	if tab.cancel != nil {
		tab.cancel()
	}
	tab.gen++
	tab.cancel, tab.client, tab.base, tab.token = nil, nil, "", ""
	tab.state = "reconnecting"
	a.remoteTabMu.Unlock()

	a.reattachRemoteTab(meta.ID)
	a.remoteTabMu.Lock()
	state := a.remoteTabs[meta.ID].state
	a.remoteTabMu.Unlock()
	if state != "ready" {
		t.Fatalf("transient EnsureServer failures parked the tab in %q", state)
	}
	if len(kernel.ensureErrs) != 0 || kernel.ensureCalls != 4 {
		t.Fatalf("reattach did not retry through both transient failures: calls=%d remaining=%d", kernel.ensureCalls, len(kernel.ensureErrs))
	}
}

// serve_down tabs parked by reattach exhaustion must revive when the host
// connection recovers — the tunnel healing is exactly what they were waiting
// for, and nothing else revisits them.
func TestResumeRemoteTabsRevivesServeDownTabs(t *testing.T) {
	serve := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: serve.server.URL, InstanceID: "serve-1"}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})

	previousDelays := remoteTabReattachDelays
	remoteTabReattachDelays = nil
	t.Cleanup(func() { remoteTabReattachDelays = previousDelays })

	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	if tab.cancel != nil {
		tab.cancel()
	}
	tab.gen++
	tab.cancel, tab.client, tab.base, tab.token = nil, nil, "", ""
	tab.state = "serve_down"
	tab.err = "Remote session reconnect failed. Retry to restart the server."
	a.remoteTabMu.Unlock()

	a.resumeRemoteTabs("box")
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.remoteTabMu.Lock()
		state := a.remoteTabs[meta.ID].state
		a.remoteTabMu.Unlock()
		if state == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host recovery left the serve_down tab in %q", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A pump whose /events connection is refused (tunnel just dropped, serve
// restarting) must route through the reattach loop rather than parking the
// tab in a terminal error state — HTTP sends still work at that point, so a
// stranded pump means replies silently never render.
func TestRemoteTabPumpConnectionFailureReattachesInsteadOfParking(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadBase := "http://" + listener.Addr().String()
	listener.Close()

	serve := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: serve.server.URL, InstanceID: "serve-1"}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})

	previousDelays := remoteTabReattachDelays
	remoteTabReattachDelays = nil
	t.Cleanup(func() { remoteTabReattachDelays = previousDelays })

	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	if tab.cancel != nil {
		tab.cancel()
	}
	pumpCtx, cancelPump := context.WithCancel(context.Background())
	tab.gen++
	tab.cancel = cancelPump
	tab.client = serve.server.Client()
	tab.base = deadBase
	tab.state = "connecting"
	gen := tab.gen
	a.remoteTabMu.Unlock()

	opened := make(chan error, 1)
	go a.remoteTabPump(pumpCtx, meta.ID, gen, opened)
	if err := <-opened; err == nil {
		t.Fatal("the refused stream should be reported to the opener")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.remoteTabMu.Lock()
		state := a.remoteTabs[meta.ID].state
		a.remoteTabMu.Unlock()
		if state == "ready" {
			break
		}
		if state == "error" {
			t.Fatal("a transiently refused stream parked the tab in error")
		}
		if time.Now().After(deadline) {
			t.Fatalf("reattach loop did not recover the refused stream, state=%q", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
