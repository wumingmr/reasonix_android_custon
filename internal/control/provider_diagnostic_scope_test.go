package control

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestProviderDiagnosticsPersistOnlyUnsuccessfulTurns(t *testing.T) {
	for _, status := range []event.TurnStatus{event.TurnCompleted, event.TurnFailed, event.TurnInterrupted, event.TurnRecoveryRequired} {
		t.Run(string(status), func(t *testing.T) {
			c := &Controller{}
			c.beginProviderDiagnosticTurn("turn")
			c.recordProviderRequest("turn", provider.RequestObservation{ID: 1, Phase: "request_started", RemoteAddress: "127.0.0.1:443"})
			events, err := c.terminalSessionEvents(event.Event{TurnID: "turn", Status: status}, session.Projection{})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range events {
				found = found || e.Kind == "diagnostic/provider"
			}
			if found != (status != event.TurnCompleted) {
				t.Fatalf("status=%s events=%+v", status, events)
			}
			if len(c.providerDiagnostics.requests) != 1 {
				t.Fatal("live observation was lost")
			}
		})
	}
}

func TestSuccessfulProviderRequestHasNoDurableTransportRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	service, err := session.NewService("local", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "successful-request"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := openai.New(provider.Config{Name: "test", Protocol: "openai", Model: "test", APIKey: "fixture", BaseURL: server.URL + "/v1", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(p, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Runner: exec, Executor: exec, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	if err := c.RunTurn(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(c.providerDiagnostics.requests) != 1 {
		t.Fatal("successful request not observed")
	}
	for _, commit := range terminationCommitHistory(t, c) {
		for _, e := range commit.Events {
			if e.Kind == "diagnostic/provider" {
				t.Fatal("successful transport evidence was persisted")
			}
		}
	}
}

func TestProviderDiagnosticTurnLossAccounting(t *testing.T) {
	c := &Controller{}
	c.beginProviderDiagnosticTurn("long")
	for id := uint64(1); id <= 130; id++ {
		c.recordProviderRequest("long", provider.RequestObservation{ID: id, Phase: "request_started"})
	}
	c.beginProviderDiagnosticTurn("next")
	for id := uint64(131); id <= 258; id++ {
		c.recordProviderRequest("next", provider.RequestObservation{ID: id, Phase: "request_started"})
	}
	c.recordProviderRequest("long", provider.RequestObservation{ID: 1, Phase: "body_closed"})
	for _, tc := range []struct {
		id       string
		dropped  uint64
		retained int
	}{{"long", 130, 0}, {"next", 0, 128}} {
		e, err := c.providerDiagnosticEvent(event.Event{TurnID: tc.id, Status: event.TurnInterrupted})
		if err != nil || e == nil {
			t.Fatalf("event=%v err=%v", e, err)
		}
		var payload struct {
			Dropped   *uint64
			Truncated bool
			Requests  []providerDiagnostic
		}
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Dropped == nil || *payload.Dropped != tc.dropped || payload.Truncated != (tc.dropped > 0) || len(payload.Requests) != tc.retained {
			t.Fatalf("%s: %s", tc.id, e.Payload)
		}
	}
	for i := range 129 {
		c.beginProviderDiagnosticTurn(fmt.Sprintf("future-%d", i))
	}
	_, dropped, truncated := c.providerDiagnosticTurnSnapshot("long")
	if len(c.providerDiagnostics.turns) != 128 || dropped != nil || !truncated {
		t.Fatal("evicted accounting was presented as complete")
	}
}
