package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func codedCode(t *testing.T, err error) (string, map[string]any) {
	t.Helper()
	var coded interface {
		error
		RPCErrorData() map[string]any
	}
	if !errors.As(err, &coded) {
		t.Fatalf("error %v is not an inboxCodedError", err)
	}
	return strings.TrimPrefix(coded.Error(), "reasonix_error:"), coded.RPCErrorData()
}

// A fence that clears by itself must be machine-distinguishable from a
// permanent refusal: the renderer keeps the message queued and retries it,
// while permanent failures still surface verbatim.
func TestInboxTargetFenceClassifiesTransientStates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*remoteTab)
	}{
		{"reconnecting", func(tab *remoteTab) { tab.state = "reconnecting" }},
		{"connecting", func(tab *remoteTab) { tab.state = "connecting" }},
		{"rehydrating", func(tab *remoteTab) { tab.routing.rehydratingPath = "/sessions/other.jsonl" }},
		{"unrouted", func(tab *remoteTab) { tab.routing.currentPath = "" }},
		{"route_moved", func(tab *remoteTab) { tab.routing.currentPath = "/sessions/other.jsonl" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, tab := remoteRuntimeTestApp(&http.Client{})
			tc.mutate(tab)
			_, err := a.CaptureInboxTarget(tab.id, runtimeRemoteTestPath)
			if err == nil {
				t.Fatal("fence admitted an unsettled tab")
			}
			code, data := codedCode(t, err)
			if code != "inbox_target_transient" || data["transient"] != true {
				t.Fatalf("code=%q data=%v, want transient", code, data)
			}
			if !strings.HasPrefix(err.Error(), "reasonix_error:"+"inbox_target_transient") {
				t.Fatalf("wire error = %q", err.Error())
			}
		})
	}
}

// The enqueue boundary classifies the same fence, so a follow-up captured
// before the tab moved is retryable rather than permanently failed.
func TestInboxEnqueueClassifiesTransientTargetMismatch(t *testing.T) {
	a, tab := remoteRuntimeTestApp(&http.Client{})
	target, err := a.CaptureInboxTarget(tab.id, runtimeRemoteTestPath)
	if err != nil {
		t.Fatalf("capture failed: %v", err)
	}
	tab.routing.rehydratingPath = "/sessions/other.jsonl"
	_, err = a.EnqueueInboxFollowupForTarget(target, "queued text", "queued text", nil, "transient-key")
	if err == nil {
		t.Fatal("enqueue admitted a rehydrating tab")
	}
	code, data := codedCode(t, err)
	if code != "inbox_target_transient" || data["transient"] != true {
		t.Fatalf("code=%q data=%v, want transient", code, data)
	}
}

// Permanent refusals keep the historical code and must not look retryable.
func TestInboxPermanentFailuresStayNonTransient(t *testing.T) {
	a, tab := remoteRuntimeTestApp(&http.Client{})
	target, err := a.CaptureInboxTarget(tab.id, runtimeRemoteTestPath)
	if err != nil {
		t.Fatalf("capture failed: %v", err)
	}
	_, err = a.EnqueueInboxFollowupForTarget(target, "text", "text", nil, "")
	if err == nil {
		t.Fatal("enqueue accepted an empty idempotency key")
	}
	code, data := codedCode(t, err)
	if code != "inbox_not_submitted" || data != nil {
		t.Fatalf("code=%q data=%v, want permanent inbox_not_submitted", code, data)
	}
}

// A settled tab still admits the same call: the classification must not turn
// the fence into a blanket refusal.
func TestInboxTargetFenceAdmitsSettledTab(t *testing.T) {
	a, tab := remoteRuntimeTestApp(&http.Client{})
	if _, err := a.CaptureInboxTarget(tab.id, runtimeRemoteTestPath); err != nil {
		t.Fatalf("settled capture failed: %v", err)
	}
}
