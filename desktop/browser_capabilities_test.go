package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/browser"
)

type recordingBrokerExecutor struct {
	*brokerFakeExecutor
	result json.RawMessage
	after  func()
}

func (e *recordingBrokerExecutor) BrowserCapability(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	if e.after != nil {
		e.after()
	}
	return e.result, nil
}

func TestBrowserBrokerRecordingPublishesOnlyTransferredCompletedArtifacts(t *testing.T) {
	for _, state := range []string{"completed", "interrupted"} {
		t.Run(state, func(t *testing.T) {
			exec := &recordingBrokerExecutor{brokerFakeExecutor: &brokerFakeExecutor{}, result: json.RawMessage(`{"state":"` + state + `","path":"/desktop/capture/video.webm"}`)}
			rig := newBrokerTestRig(t, sessionResolver(exec, "/ws", map[string]bool{"/s": true}))
			rig.conn = fakeSFTPConn{}
			rig.relayTo = "/remote/scratch/"
			token, _, err := rig.broker.register("host", rig.gen)
			if err != nil {
				t.Fatal(err)
			}
			client := browser.NewHTTPExecutor(rig.baseURL, token, nil).(browser.CapabilityExecutor)
			call := func() (json.RawMessage, error) {
				return client.BrowserCapability(browser.WithSession(context.Background(), "/s"), "record", json.RawMessage(`{"tabId":"tab-1","action":"status","recordingId":"r"}`))
			}
			result, err := call()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(result), "/desktop/") {
				t.Fatalf("desktop path leaked: %s", result)
			}
			if state == "completed" && (!strings.Contains(string(result), "/remote/scratch/video.webm") || len(rig.relayCalls()) != 1) {
				t.Fatalf("artifact not relayed: %s", result)
			}
			if state == "interrupted" && len(rig.relayCalls()) != 0 {
				t.Fatal("interrupted file was published")
			}
			exec.after = func() { rig.mu.Lock(); rig.live = false; rig.mu.Unlock() }
			if result, err := call(); err == nil || len(result) != 0 {
				t.Fatalf("late generation result was accepted: %s %v", result, err)
			}
		})
	}
}
