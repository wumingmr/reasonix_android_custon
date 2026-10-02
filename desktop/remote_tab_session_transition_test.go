package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRemoteResumeBusyKeepsCurrentSessionReady(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "saved", Path: "/saved.jsonl", Title: "Saved"}})
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	fs.mu.Lock()
	fs.failEnter = "cannot resume while a turn is running"
	fs.mu.Unlock()
	if _, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{SessionName: "saved"}); err != nil {
		t.Fatal(err)
	}
	waitForRemoteTabError(t, a, meta.ID, "Finish the current turn")
	a.remoteTabMu.Lock()
	state, message := a.remoteTabs[meta.ID].state, a.remoteTabs[meta.ID].err
	a.remoteTabMu.Unlock()
	if state != "ready" || !strings.Contains(message, "Finish the current turn") {
		t.Fatalf("busy resume state/error = %q/%q, want ready non-terminal notice", state, message)
	}
}

func TestRemoteResumeRejectedKeepsCurrentSessionReady(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "saved", Path: "/saved.jsonl", Title: "Saved"}})
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	fs.mu.Lock()
	fs.failEnter = "session is already leased by another process"
	fs.mu.Unlock()
	a.resumeRemoteTabSession(meta.ID, "saved")
	a.remoteTabMu.Lock()
	state, message := a.remoteTabs[meta.ID].state, a.remoteTabs[meta.ID].err
	a.remoteTabMu.Unlock()
	if state != "ready" || !strings.Contains(message, "already leased") {
		t.Fatalf("rejected resume state/error = %q/%q, want ready action error", state, message)
	}
}

func TestRemoteResumeRejectedRestoresForegroundRoute(t *testing.T) {
	const oldPath = "/old.jsonl"
	const targetPath = "/target.jsonl"
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{
		{Name: "old", Path: oldPath, Current: true},
		{Name: "target", Path: targetPath},
	})
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{})
	fs.mu.Lock()
	fs.failEnter = "session is already leased by another process"
	fs.mu.Unlock()
	a.resumeRemoteTabSessionPath(meta.ID, "target", targetPath, "Target")
	a.remoteTabMu.Lock()
	got := a.remoteTabs[meta.ID].routing.currentPath
	a.remoteTabMu.Unlock()
	if got != oldPath {
		t.Fatalf("foreground route after rejected resume = %q, want %q", got, oldPath)
	}
}

func TestRemoteResumeBuffersTargetFramesUntilPostCommit(t *testing.T) {
	const oldPath = "/old.jsonl"
	const targetPath = "/target.jsonl"
	feed := make(chan string, 4)
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{
		{Name: "old", Path: oldPath, Current: true},
		{Name: "target", Path: targetPath, Running: true},
	})
	fs.mu.Lock()
	fs.eventFrames = []string{`{"kind":"ready","sessionPath":"/old.jsonl"}`}
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
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	log := &eventLog{}
	a := &App{remoteRuntime: kernel, remoteEventHook: log.add}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{})
	eventPrefix := "remote-tab:" + meta.ID + ":event"
	readyPrefix := "remote-tab:" + meta.ID + ":state"
	waitForRemoteEventCount(t, log, readyPrefix, 2)
	eventsBefore, readyBefore := log.count(eventPrefix), log.count(readyPrefix)
	done := make(chan struct{})
	go func() {
		a.resumeRemoteTabSessionPath(meta.ID, "target", targetPath, "Target")
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resume request did not start")
	}
	// Until /resume commits, Serve's authoritative status is still the old
	// foreground. A poll from the runtime watchdog must not roll the provisional
	// target route back and create a second target ready barrier.
	_, _ = a.RemoteTabStatus(meta.ID)
	a.remoteTabMu.Lock()
	provisionalPath := a.remoteTabs[meta.ID].routing.currentPath
	rehydratingPath := a.remoteTabs[meta.ID].routing.rehydratingPath
	a.remoteTabMu.Unlock()
	if provisionalPath != targetPath || rehydratingPath != targetPath {
		t.Fatalf("old status rolled back provisional route: current/rehydrating = %q/%q", provisionalPath, rehydratingPath)
	}
	feed <- `{"kind":"approval_request","approval":{"id":"target-approval"},"sessionPath":"/target.jsonl","sessionCurrent":true}`
	feed <- `{"kind":"text","text":"first retained delta","sessionPath":"/target.jsonl","sessionCurrent":true}`
	feed <- `{"kind":"notice","text":"second retained notice","sessionPath":"/target.jsonl","sessionCurrent":true}`
	deadline := time.Now().Add(time.Second)
	for {
		a.remoteTabMu.Lock()
		pending := len(a.remoteTabs[meta.ID].pendingEvents)
		buffered := len(a.remoteTabs[meta.ID].routing.rehydratingFrames)
		a.remoteTabMu.Unlock()
		if pending == 1 && buffered == 3 {
			break
		}
		select {
		case <-done:
			t.Fatal("resume returned before the test released its response")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("target frames were not retained while /resume was pending: pending=%d buffered=%d log=%v", pending, buffered, log.recorded())
		}
		time.Sleep(time.Millisecond)
	}
	if got := log.count(eventPrefix); got != eventsBefore {
		t.Fatalf("provisional target frame reached the old transcript: events %d -> %d, log=%v", eventsBefore, got, log.recorded())
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resume did not finish")
	}
	if got := log.count(readyPrefix); got != readyBefore+1 {
		t.Fatalf("committed resume emitted %d ready barriers, want %d: %v", got, readyBefore+1, log.recorded())
	}
	events := log.recorded()
	readyIndex, approvalIndex, firstIndex, secondIndex := -1, -1, -1, -1
	for i, got := range events {
		if strings.HasPrefix(got, readyPrefix+" ") {
			readyIndex = i
		}
		if strings.Contains(got, `"id":"target-approval"`) {
			approvalIndex = i
		}
		if strings.Contains(got, `"text":"first retained delta"`) {
			firstIndex = i
		}
		if strings.Contains(got, `"text":"second retained notice"`) {
			secondIndex = i
		}
	}
	if readyIndex < 0 || approvalIndex <= readyIndex || firstIndex <= approvalIndex || secondIndex <= firstIndex {
		t.Fatalf("buffered target frames were not replayed in order after ready: %v", events)
	}
	a.remoteTabMu.Lock()
	pending := len(a.remoteTabs[meta.ID].pendingEvents)
	rehydrating := a.remoteTabs[meta.ID].routing.rehydratingPath
	a.remoteTabMu.Unlock()
	if pending != 1 || rehydrating != "" {
		t.Fatalf("committed target pending/rehydrating = %d/%q, want 1/empty", pending, rehydrating)
	}
}

func TestRemoteNewBusyKeepsCurrentSessionReady(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "saved", Path: "/saved.jsonl", Title: "Saved", Current: true}})
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{SessionName: "saved"})
	a.remoteTabMu.Lock()
	previousTitle := a.remoteTabs[meta.ID].topicTitle
	previousSession := a.remoteTabs[meta.ID].session
	previousRoute := a.remoteTabs[meta.ID].routing.currentPath
	previousRuntime := a.remoteTabs[meta.ID].runtime
	a.remoteTabMu.Unlock()
	fs.mu.Lock()
	fs.failEnter = "cannot start a new session while a turn is running"
	fs.mu.Unlock()
	if _, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true}); err == nil || !strings.Contains(err.Error(), "while a turn is running") {
		t.Fatalf("busy new-session error = %v", err)
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	state, message, title, client := tab.state, tab.err, tab.topicTitle, tab.client
	session, route, runtime := tab.session, tab.routing.currentPath, tab.runtime
	a.remoteTabMu.Unlock()
	if state != "ready" || message != "" || title != previousTitle || client == nil {
		t.Fatalf("busy new-session state/error/title/client = %q/%q/%q/%v, want ready current attachment", state, message, title, client)
	}
	if session != previousSession || route != previousRoute || !reflect.DeepEqual(runtime, previousRuntime) {
		t.Fatalf("busy new-session changed current identity/runtime: session=%+v route=%q runtime=%+v", session, route, runtime)
	}
}

func TestRemoteResumeLeaseConflictFailsAttach(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "saved", Path: "/saved.jsonl", Title: "Saved"}})
	fs.mu.Lock()
	fs.failEnter = "this session is in use by another Reasonix window or process"
	fs.mu.Unlock()
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{SessionName: "saved"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "error")
	a.remoteTabMu.Lock()
	state, message, client := a.remoteTabs[meta.ID].state, a.remoteTabs[meta.ID].err, a.remoteTabs[meta.ID].client
	a.remoteTabMu.Unlock()
	if state != "error" || !strings.Contains(message, "session is in use") || client != nil {
		t.Fatalf("lease-conflict attach state/error/client = %q/%q/%v", state, message, client)
	}
}

func TestRemoteResumeListFailureKeepsCurrentAttachmentReady(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "saved", Path: "/saved.jsonl", Title: "Saved"}})
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	fs.mu.Lock()
	fs.failSessions = true
	fs.mu.Unlock()
	a.resumeRemoteTabSession(meta.ID, "saved")
	a.remoteTabMu.Lock()
	state, message := a.remoteTabs[meta.ID].state, a.remoteTabs[meta.ID].err
	a.remoteTabMu.Unlock()
	if state != "ready" || !strings.Contains(message, "Could not open remote session") {
		t.Fatalf("list failure state/error = %q/%q, want ready non-terminal notice", state, message)
	}
}
