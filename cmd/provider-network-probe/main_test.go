package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyntheticProbeIsBoundedAndDoesNotFollowRedirects(t *testing.T) {
	for _, size := range []int{1024, 3_248_572} {
		body := syntheticBody("test-model", size)
		var payload map[string]any
		if len(body) != size || json.Unmarshal(body, &payload) != nil || payload["max_tokens"] != float64(1) || payload["tools"] != nil {
			t.Fatalf("invalid synthetic request length=%d", len(body))
		}
	}
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		http.Redirect(w, r, "/redirect", http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	t.Setenv("REASONIX_PROBE_API_KEY", "local-only-key")
	if err := run(s.URL, "test-model", "h1", 1024, false, time.Second); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	tr := &singleTransport{base: http.DefaultTransport}
	tr.used.Store(true)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.URL, nil)
	if _, err := tr.RoundTrip(req); err == nil || calls.Load() != 1 {
		t.Fatal("probe allowed a retry")
	}
}
