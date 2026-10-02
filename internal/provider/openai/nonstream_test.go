package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// TestStreamHTMLResponseDiagnostic reproduces #8781: a gateway that answers a
// POST to its root path with 200 + an HTML landing page (no SSE events at all).
// The stream must surface a diagnostic naming the non-streaming response and
// the Content-Type, instead of a bare "unexpected EOF" that reads like a
// dropped connection.
func TestStreamHTMLResponseDiagnostic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<!doctype html><html><head><title>Gateway</title></head><body>Welcome</body></html>")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "deepseek-responses", BaseURL: srv.URL, Model: "deepseek-v4", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var gotErr error
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			gotErr = chunk.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected a stream error for an HTML response, got none")
	}
	msg := gotErr.Error()
	for _, want := range []string{
		"non-streaming response",
		"text/html",
		"request_url/base_url",
		"<!doctype html>",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	if !errors.Is(gotErr, provider.ErrNonStreamingResponse) {
		t.Errorf("error %v does not carry ErrNonStreamingResponse", gotErr)
	}
	if errors.Is(gotErr, io.ErrUnexpectedEOF) || provider.IsConnReset(gotErr) || provider.IsStreamInterrupted(gotErr) {
		t.Errorf("error %v is classified as a dropped connection", gotErr)
	}
}

// TestStreamHTMLResponseDiagnosticJSONError covers the sibling case where the
// root path answers 200 with a JSON error page rather than HTML.
func TestStreamHTMLResponseDiagnosticJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"error":"not found"}`)
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "gw", BaseURL: srv.URL, Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var gotErr error
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			gotErr = chunk.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected a stream error for a JSON response, got none")
	}
	msg := gotErr.Error()
	if !strings.Contains(msg, "non-streaming response") || !strings.Contains(msg, "application/json") {
		t.Errorf("error %q missing non-streaming diagnostic", msg)
	}
	if !errors.Is(gotErr, provider.ErrNonStreamingResponse) || errors.Is(gotErr, io.ErrUnexpectedEOF) {
		t.Errorf("error %v: want ErrNonStreamingResponse identity without io.ErrUnexpectedEOF", gotErr)
	}
}

// TestStreamMissingContentTypeStillStreams guards the diagnostic: a gateway
// that streams SSE without setting Content-Type must keep working.
func TestStreamMissingContentTypeStillStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // no Content-Type header at all
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "gw", BaseURL: srv.URL, Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got strings.Builder
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		if chunk.Type == provider.ChunkText {
			got.WriteString(chunk.Text)
		}
	}
	if got.String() != "ok" {
		t.Errorf("streamed text = %q, want %q", got.String(), "ok")
	}
}

func TestIsEventStreamContentType(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"text/event-stream", true},
		{"text/event-stream; charset=utf-8", true},
		{"application/x-ndjson", true},
		{"text/html", false},
		{"application/json", false},
		{"", true}, // unknown: diagnostics only, never reject
	}
	for _, tc := range tests {
		if got := isEventStreamContentType(tc.in); got != tc.want {
			t.Errorf("isEventStreamContentType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
