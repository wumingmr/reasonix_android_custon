package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/serve"
	"reasonix/internal/servecontract"
	"reasonix/internal/sessioninbox"
)

func TestRemoteInboxQueueCapabilitiesAndIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Sink: event.Discard})
	defer ctrl.Close()
	if err := ctrl.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	receipt, err := ctrl.EnqueueInbox(control.InboxRequest{Submit: "remote full text", Idempotency: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(operatorServeHandler(serve.New(ctrl, nil, config.ServeConfig{})))
	defer server.Close()
	a, tab := remoteRuntimeTestApp(server.Client())
	tab.base, tab.routing.currentPath, tab.session.path = server.URL, path, path
	target, err := a.CaptureInboxTarget(tab.id, path)
	if err != nil {
		t.Fatal(err)
	}
	request := control.InboxQueueRequest{Kind: "read", ItemID: receipt.ItemID}
	unsupported, err := a.InboxQueueForTarget(target, request)
	if err != nil || unsupported.Reason != "unsupported" {
		t.Fatalf("old remote: %+v %v", unsupported, err)
	}
	tab.capabilities[servecontract.InboxMutationsV1] = true
	read, err := a.InboxQueueForTarget(target, request)
	if err != nil || read.Edit == nil || read.Edit.Text != "remote full text" {
		t.Fatalf("read: %+v %v", read, err)
	}
	request = control.InboxQueueRequest{Kind: "edit", ItemID: receipt.ItemID, ContentVersion: read.Edit.ContentVersion, Text: "edited remote"}
	saved, err := a.InboxQueueForTarget(target, request)
	if err != nil || saved.Outcome != "applied" || !saved.Snapshot.Paused {
		t.Fatalf("save: %+v %v", saved, err)
	}
	tab.selectionRevision++
	stale, err := a.InboxQueueForTarget(target, request)
	if err != nil || stale.Reason != "session_changed" {
		t.Fatalf("stale target: %+v %v", stale, err)
	}
	_, env, _ := ctrl.ReadInboxItem(receipt.ItemID)
	if env.SubmitText != "edited remote" {
		t.Fatal("remote queue changed unexpectedly")
	}
	tab.selectionRevision--
	ctrl.SetSessionPath(filepath.Join(dir, "other.jsonl"))
	stale, err = a.InboxQueueForTarget(target, control.InboxQueueRequest{Kind: "pause", Paused: true})
	if err != nil || stale.Reason != "session_changed" {
		t.Fatalf("remote foreground switch: %+v %v", stale, err)
	}
}

func TestTargetGuidanceQueuesEndedTurnAndRejectsReplacementSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Sink: event.Discard})
	cleanupExactTurnController(t, ctrl)
	if err := ctrl.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "tab", Ctrl: ctrl, SessionPath: path, SessionGeneration: 1, Ready: true}
	a := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}
	target, err := a.CaptureInboxTarget("tab", path)
	if err != nil {
		t.Fatal(err)
	}
	request := control.InboxQueueRequest{Kind: "enqueue_steer", TurnID: "ended-turn", Text: "preserve guidance", Display: "preserve guidance", IdempotencyKey: "one-guidance"}
	result, err := a.InboxQueueForTarget(target, request)
	if err != nil || result.Receipt == nil || result.Receipt.Disposition != sessioninbox.DispositionQueuedFollowup {
		t.Fatalf("ended turn: %+v %v", result, err)
	}
	duplicate, err := a.InboxQueueForTarget(target, request)
	if err != nil || duplicate.Receipt == nil || duplicate.Receipt.ItemID != result.Receipt.ItemID || len(ctrl.InboxSnapshot().Items) != 1 {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	confirmed, err := a.LookupInboxFollowupForTarget(target, request.IdempotencyKey)
	if err != nil || confirmed.ItemID != result.Receipt.ItemID {
		t.Fatalf("receipt recovery: %+v %v", confirmed, err)
	}
	tab.SessionGeneration++
	request.IdempotencyKey = "stale-input"
	stale, err := a.InboxQueueForTarget(target, request)
	if err != nil || stale.Reason != "session_changed" || len(ctrl.InboxSnapshot().Items) != 1 {
		t.Fatalf("replacement fence: %+v %v", stale, err)
	}
	// Legacy tab-only calls cannot prove session ownership and keep their guard.
	if _, err := a.EnqueueInboxSteerForTurn("tab", "ended-turn", "unsafe", "unsafe", "unsafe"); err == nil {
		t.Fatal("legacy request bypassed the session fence")
	}
}

// operatorServeHandler stands in for the remote client, which holds the
// launch token a serve requires for mutations.
func operatorServeHandler(s *serve.Server) http.Handler {
	h := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+s.AuthToken())
		h.ServeHTTP(w, r)
	})
}
