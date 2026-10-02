package agent

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
)

func TestSessionReportsProviderFailuresWithoutRetry(t *testing.T) {
	for _, history := range []string{"new", "legacy"} {
		for _, connection := range []string{"valid", "invalid-header", "unavailable"} {
			t.Run(history+"/"+connection, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if connection == "unavailable" {
						w.Header().Set("Retry-After", "120")
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `{"error":{"message":"temporarily unavailable"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()
				sess := NewSession("system")
				if history == "legacy" {
					path := filepath.Join(t.TempDir(), "legacy.jsonl")
					if err := os.WriteFile(path, []byte(legacySessionFixture), 0o600); err != nil {
						t.Fatal(err)
					}
					var err error
					sess, err = LoadSession(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				cfg := provider.Config{
					Name: "deepseek", Model: "deepseek-v4-flash", BaseURL: server.URL, HTTPClient: server.Client(),
					Extra: map[string]any{"reasoning_protocol": "deepseek"},
				}
				if connection == "invalid-header" {
					cfg.Extra["headers"] = map[string]string{"bad header": "value"}
				}
				p, err := openai.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				sink := &recordSink{}
				a := New(p, echoRegistry(), sess, Options{}, sink)
				err = a.Run(withNoClosedLoop(t.Context()), "continue")
				if connection == "invalid-header" {
					var failure *provider.RequestFailure
					if !errors.As(err, &failure) || !strings.Contains(err.Error(), "invalid header field name") || provider.AsRecoveryWaitExhausted(err) != nil {
						t.Fatalf("invalid request must preserve its cause without waiting: %v", err)
					}
				} else if connection == "unavailable" {
					var failure *provider.APIError
					if !errors.As(err, &failure) || failure.Status != http.StatusServiceUnavailable || provider.AsRecoveryWaitExhausted(err) != nil {
						t.Fatalf("upstream error must return without waiting: %v", err)
					}
				} else if err != nil || len(sink.kinds(event.Text)) == 0 {
					t.Fatalf("session failed to continue: %v", err)
				}
				if retries := sink.kinds(event.Retrying); len(retries) != 0 {
					t.Fatalf("unexpected network recovery: %+v", retries)
				}
			})
		}
	}
}
