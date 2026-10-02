package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestObservationDistinguishesHeaderAndBodyWaitCancellation(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[headers], func(t *testing.T) {
			arrived := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if headers {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, ": ping\n\n")
					w.(http.Flusher).Flush()
				}
				close(arrived)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var mu sync.Mutex
			var observations []RequestObservation
			ctx = WithRequestObserver(ctx, func(v RequestObservation) { mu.Lock(); observations = append(observations, v); mu.Unlock() })
			snapshot := func() RequestObservation { mu.Lock(); defer mu.Unlock(); return observations[len(observations)-1] }
			done := make(chan error, 1)
			bodyRead := make(chan struct{})
			go func() {
				resp, err := SendWithRetry(ctx, server.Client(), SendOptions{}, func(ctx context.Context) (*http.Request, error) {
					return http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/?secret=private-token", nil)
				})
				if err == nil {
					defer resp.Body.Close()
					_, err = io.ReadFull(resp.Body, make([]byte, len(": ping\n\n")))
					close(bodyRead)
					if err == nil {
						_, err = resp.Body.Read(make([]byte, 1))
					}
				}
				done <- err
			}()
			select {
			case <-arrived:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if headers {
				select {
				case <-bodyRead:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			before := snapshot()
			if headers {
				if before.HeadersAt.IsZero() || before.FirstBodyAt.IsZero() || before.BodyBytes != 8 || !before.FinishedAt.IsZero() {
					t.Fatalf("body wait: %+v", before)
				}
			} else if !before.HeadersAt.IsZero() || before.BodyBytes != 0 || !before.FinishedAt.IsZero() {
				t.Fatalf("header wait: %+v", before)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation stalled")
			}
			after := snapshot()
			if after.Phase != "canceled" || after.FinishedAt.IsZero() || before.ID != after.ID {
				t.Fatalf("terminal observation: %+v", after)
			}
			encoded, _ := json.Marshal(after)
			if strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), server.URL) {
				t.Fatal("diagnostics leaked transport content")
			}
		})
	}
}

func TestRequestObservationPreservesResponseBytesAndEOF(t *testing.T) {
	const body = "data: {\"content\":\"hello\"}\n\ndata: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
	defer server.Close()
	var mu sync.Mutex
	var last RequestObservation
	ctx := WithRequestObserver(t.Context(), func(v RequestObservation) { mu.Lock(); last = v; mu.Unlock() })
	resp, err := SendWithRetry(ctx, server.Client(), SendOptions{}, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(got) != body {
		t.Fatalf("body changed: %q %v", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if last.Phase != "body_eof" || last.BodyBytes != int64(len(body)) {
		t.Fatalf("observation: %+v", last)
	}
}
