package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

type exactPromptController struct {
	control.SessionAPI
	sessionID string
	calls     []control.PromptIdentity
}

func (c *exactPromptController) RuntimeStateSnapshot() event.RuntimeStateSnapshot {
	return event.RuntimeStateSnapshot{SchemaVersion: 1, SessionID: c.sessionID}
}

func (c *exactPromptController) ResolvePromptExact(identity control.PromptIdentity, _ control.PromptAnswer) error {
	c.calls = append(c.calls, identity)
	return nil
}

func TestServeExactPromptChecksSessionBeforeController(t *testing.T) {
	bc := NewBroadcaster()
	ctrl := &exactPromptController{SessionAPI: control.New(control.Options{Sink: bc}), sessionID: "session-a"}
	srv := httptest.NewServer(operatorHandler(New(ctrl, bc, config.ServeConfig{})))
	defer srv.Close()
	post := func(sessionID string) int {
		resp, err := http.Post(srv.URL+"/resolve-prompt", "application/json", strings.NewReader(
			`{"sessionId":"`+sessionID+`","promptId":"p1","turnId":"t1","runtimeEpoch":"r1","kind":"ask","answer":{"questions":[]}}`,
		))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("session-old"); got != http.StatusConflict || len(ctrl.calls) != 0 {
		t.Fatalf("stale exact prompt status/calls = %d/%d", got, len(ctrl.calls))
	}
	if got := post("session-a"); got != http.StatusNoContent || len(ctrl.calls) != 1 || ctrl.calls[0].RuntimeEpoch != "r1" {
		t.Fatalf("current exact prompt status/calls = %d/%+v", got, ctrl.calls)
	}
}
