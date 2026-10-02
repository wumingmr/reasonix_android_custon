package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type enhancedHTTPFake struct {
	*fakeExecutor
	session string
	calls   int
}

func (f *enhancedHTTPFake) BrowserCapability(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	f.session = SessionFromContext(ctx)
	f.calls++
	return json.RawMessage(`{"count":2,"ambiguous":true}`), nil
}
func TestCapabilityRemoteNegotiationAndNoReplay(t *testing.T) {
	fake := &enhancedHTTPFake{fakeExecutor: &fakeExecutor{}}
	client, _ := newRoundTrip(t, fake)
	result, err := client.(CapabilityExecutor).BrowserCapability(WithSession(context.Background(), "session"), "query", json.RawMessage(`{"tabId":"tab-1"}`))
	if err != nil || !json.Valid(result) || fake.session != "session" || fake.calls != 1 {
		t.Fatalf("%s %v %+v", result, err, fake)
	}
	old, _ := newRoundTrip(t, &fakeExecutor{})
	_, err = old.(CapabilityExecutor).BrowserCapability(context.Background(), "record", json.RawMessage(`{"action":"start"}`))
	if !errors.Is(err, errCapabilityUnsupported) || errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("old host: %v", err)
	}
	for _, status := range []int{404, 500} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status) }))
		remote := NewHTTPExecutor(server.URL, "token", server.Client()).(CapabilityExecutor)
		_, err = remote.BrowserCapability(context.Background(), "pointer", json.RawMessage(`{"action":"click"}`))
		server.Close()
		if calls != 1 {
			t.Fatalf("write replayed %d times", calls)
		}
		if status == 404 && !errors.Is(err, errCapabilityUnsupported) || status == 500 && !errors.Is(err, ErrUnknownOutcome) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}

func TestCapabilityCancelledWritePreservesUnknownAndTransportCause(t *testing.T) {
	for _, name := range []string{"pointer", "viewport", "record"} {
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
			}))
			defer server.Close()
			defer close(release)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			remote := NewHTTPExecutor(server.URL, "token", server.Client()).(CapabilityExecutor)
			done := make(chan error, 1)
			go func() {
				_, err := remote.BrowserCapability(ctx, name, json.RawMessage(`{"action":"start"}`))
				done <- err
			}()
			<-entered // The write reached the server; cancellation cannot prove no effect.
			cancel()
			err := <-done
			var transport *transportError
			if !errors.Is(err, ErrUnknownOutcome) || !errors.Is(err, context.Canceled) || !errors.As(err, &transport) {
				t.Fatalf("lost unknown outcome or underlying cancellation: %v", err)
			}
		})
	}
}
