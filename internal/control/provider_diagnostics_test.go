package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

type diagnosticRequestRunner struct {
	client *http.Client
	url    string
}

func (r diagnosticRequestRunner) Run(ctx context.Context, _ string) error {
	resp, err := provider.SendWithRetry(ctx, r.client, provider.SendOptions{}, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func TestModelTurnRecordsProviderTransportEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, ": ping\n\n") }))
	defer server.Close()
	c := newOwnedTestController(t, Options{Runner: diagnosticRequestRunner{server.Client(), server.URL}, Sink: event.Discard, SessionDir: t.TempDir()})
	if err := c.RunTurn(t.Context(), "observe transport"); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c.providerDiagnosticSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct{ Requests []providerDiagnostic }
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Requests) != 1 {
		t.Fatalf("missing observer wiring: %s", data)
	}
	r := snapshot.Requests[0]
	if r.TurnID == "" || r.Phase != "body_eof" || r.HeadersAt.IsZero() || r.BodyBytes != 8 {
		t.Fatalf("incomplete transport evidence: %+v", r)
	}
}

func TestProviderDiagnosticsBoundRequestsNotHeartbeatEvents(t *testing.T) {
	c := &Controller{}
	for id := uint64(1); id <= 130; id++ {
		c.recordProviderRequest("turn", provider.RequestObservation{ID: id, Phase: "request_started"})
		for i := 1; i <= 300; i++ {
			c.recordProviderRequest("turn", provider.RequestObservation{ID: id, Phase: "body_received", BodyBytes: int64(i)})
		}
	}
	c.recordProviderRequest("old", provider.RequestObservation{ID: 1, Phase: "body_closed"})
	var snapshot struct {
		Requests []providerDiagnostic
		Dropped  uint64
	}
	data, err := json.Marshal(c.providerDiagnosticSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Requests) != 128 || snapshot.Dropped != 2 || snapshot.Requests[0].ID != 3 {
		t.Fatalf("bounded snapshot: %s", data)
	}
	last := snapshot.Requests[127]
	if last.TurnID != "turn" || last.BodyBytes != 300 {
		t.Fatalf("latest observation: %+v", last)
	}
	c.recordProviderRequest("turn", provider.RequestObservation{ID: 130, Phase: "body_closed"})
	if last.Phase != "body_received" {
		t.Fatal("snapshot was mutated")
	}
}
