package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/serve"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestCanonicalInboxTargetEnqueuesAndConfirmsReceipt(t *testing.T) {
	isolateDesktopUserDirs(t)
	service, err := session.NewService("local", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	executor := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Executor: executor, SessionService: service, Sink: event.Discard})
	t.Cleanup(func() {
		ctrl.Close()
		<-ctrl.Closed()
		_ = service.CloseAll(context.Background())
	})
	if _, err := ctrl.BindFreshSession(t.Context(), "queue-target"); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	const path = "session-id:queue-target"
	a := &App{tabs: map[string]*WorkspaceTab{"tab": {ID: "tab", Ctrl: ctrl, SessionID: "queue-target", SessionGeneration: 1, Ready: true}}}
	target, err := a.CaptureInboxTarget("tab", path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := a.EnqueueInboxFollowupForTarget(target, "queued input", "queued input", nil, "request")
	if err != nil || receipt.ItemID == "" {
		t.Fatalf("canonical enqueue: %+v %v", receipt, err)
	}
	queue, err := a.InboxQueueForTarget(target, control.InboxQueueRequest{Kind: "enqueue_steer", TurnID: "finished-turn", Text: "guidance", Display: "guidance", IdempotencyKey: "guidance-request"})
	if err != nil || queue.Receipt == nil || queue.Receipt.ItemID == "" {
		t.Fatalf("canonical guidance: %+v %v", queue, err)
	}
	confirmed, err := a.LookupInboxFollowupForTarget(target, "request")
	if err != nil || confirmed.ItemID != receipt.ItemID {
		t.Fatalf("canonical confirmation: %+v %v", confirmed, err)
	}
	a.tabs["tab"].SessionGeneration++
	if _, err := a.EnqueueInboxFollowupForTarget(target, "stale input", "stale input", nil, "stale"); err == nil {
		t.Fatal("stale generation accepted a new write")
	}
	if confirmed, err := a.LookupInboxFollowupForTarget(target, "request"); err != nil || confirmed.ItemID != receipt.ItemID {
		t.Fatalf("same-session receipt after rebind: %+v %v", confirmed, err)
	}
}

func TestRemoteInboxTargetLostReceiptOnlyQueriesOriginalRequest(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Sink: event.Discard})
	defer ctrl.Close()
	if err := ctrl.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(operatorServeHandler(serve.New(ctrl, nil, config.ServeConfig{})))
	defer server.Close()
	posts, lookups := 0, 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			posts++
			resp, err := http.DefaultTransport.RoundTrip(req)
			if err != nil {
				return nil, err
			}
			resp.Body.Close()
			return nil, errors.New("accepted POST reply lost")
		}
		lookups++
		if req.URL.Query().Get("session") != path || req.URL.Query().Get("key") != "original-request" {
			t.Errorf("receipt request changed identity: %s", req.URL.Path)
		}
		if lookups == 1 {
			return nil, errors.New("first receipt response lost")
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
	a, tab := remoteRuntimeTestApp(client)
	tab.base, tab.routing.currentPath, tab.session.path = server.URL, path, path
	target, err := a.CaptureInboxTarget(tab.id, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnqueueInboxFollowupForTarget(target, "original input", "original input", nil, "original-request"); err == nil {
		t.Fatal("expected uncertain response")
	}
	if len(ctrl.InboxSnapshot().Items) != 1 {
		t.Fatal("POST was not durably accepted")
	}
	for range 3 {
		receipt, err := a.LookupInboxFollowupForTarget(target, "original-request")
		if err != nil || receipt.ItemID == "" {
			t.Fatalf("lookup = %+v, %v", receipt, err)
		}
	}
	if posts != 1 || lookups != 4 || len(ctrl.InboxSnapshot().Items) != 1 {
		t.Fatalf("lookup replayed POST: posts=%d lookups=%d", posts, lookups)
	}
	for _, change := range []string{"selection", "generation", "path", "host"} {
		t.Run(change, func(t *testing.T) {
			oldGen, oldSelection, oldPath, oldHost := tab.gen, tab.selectionRevision, tab.routing.currentPath, tab.ref.HostID
			defer func() {
				tab.gen, tab.selectionRevision, tab.routing.currentPath, tab.ref.HostID = oldGen, oldSelection, oldPath, oldHost
			}()
			switch change {
			case "selection":
				tab.selectionRevision++
			case "generation":
				tab.gen++
			case "path":
				tab.routing.currentPath = "other"
			case "host":
				tab.ref.HostID = "different-host"
			}
			before := lookups
			_, lookupErr := a.LookupInboxFollowupForTarget(target, "original-request")
			if change == "path" || change == "host" {
				if lookupErr == nil || lookups != before {
					t.Fatal("lookup accepted different session or host")
				}
			} else if lookupErr != nil || lookups != before+1 {
				t.Fatalf("same-session read did not recover after reconnect: %v", lookupErr)
			}
			if _, err := a.EnqueueInboxFollowupForTarget(target, "new", "new", nil, "new"); err == nil || !strings.Contains(err.Error(), "inbox_target_transient") {
				t.Fatalf("stale target was not explicitly rejected: %v", err)
			}
			if posts != 1 {
				t.Fatal("stale write target crossed network boundary")
			}
		})
	}
}

func TestLocalInboxTargetRejectsReplacementAndPreservesReceipt(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Sink: event.Discard})
	defer ctrl.Close()
	if err := ctrl.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	a := &App{tabs: map[string]*WorkspaceTab{"tab": {ID: "tab", Ctrl: ctrl, SessionPath: path, SessionGeneration: 1, Ready: true}}}
	target, err := a.CaptureInboxTarget("tab", path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := a.EnqueueInboxFollowupForTarget(target, "input", "input", nil, "local-request")
	if err != nil || receipt.ItemID == "" {
		t.Fatalf("enqueue = %+v, %v", receipt, err)
	}
	confirmed, err := a.LookupInboxFollowupForTarget(target, "local-request")
	if err != nil || confirmed.ItemID != receipt.ItemID {
		t.Fatalf("lookup = %+v, %v", confirmed, err)
	}
	if _, err := a.LookupInboxFollowupForTarget(target, "unknown"); err == nil {
		t.Fatal("missing receipt reported as confirmed")
	}
	a.tabs["tab"].SessionGeneration++
	if _, err := a.EnqueueInboxFollowupForTarget(target, "replacement", "replacement", nil, "replacement"); err == nil {
		t.Fatal("replacement accepted old request")
	}
	if got, err := a.LookupInboxFollowupForTarget(target, "local-request"); err != nil || got.ItemID != receipt.ItemID {
		t.Fatalf("same-session read could not rebind: %+v %v", got, err)
	}
	if len(ctrl.InboxSnapshot().Items) != 1 {
		t.Fatal("replacement created another item")
	}
}

func TestRemoteInboxReceiptRejectsSelectionChangedDuringRead(t *testing.T) {
	isolateDesktopUserDirs(t)
	var a *App
	var tab *remoteTab
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		a.remoteTabMu.Lock()
		tab.selectionRevision++
		a.remoteTabMu.Unlock()
		return remoteRuntimeTestResponse(req, 200, `{"itemId":"old-item","position":0,"disposition":"idempotent_hit","paused":false}`), nil
	})}
	a, tab = remoteRuntimeTestApp(client)
	target, err := a.CaptureInboxTarget(tab.id, runtimeRemoteTestPath)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := a.LookupInboxFollowupForTarget(target, "original"); err == nil || receipt.ItemID != "" {
		t.Fatalf("stale read escaped its selection fence: %+v, %v", receipt, err)
	}
}
