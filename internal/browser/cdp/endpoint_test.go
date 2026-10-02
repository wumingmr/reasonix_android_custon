package cdp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWebSocketURLRefusesNonLoopbackByDefault(t *testing.T) {
	ctx := context.Background()
	for _, endpoint := range []string{"http://10.0.0.5:9222", "ws://example.test:9222/devtools/browser/x", "http://[2001:db8::1]:9222"} {
		if _, err := resolveWebSocketURL(ctx, endpoint, nil, false); err == nil {
			t.Fatalf("resolveWebSocketURL(%q) accepted a remote DevTools endpoint", endpoint)
		} else if !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("resolveWebSocketURL(%q) failed for the wrong reason: %v", endpoint, err)
		}
	}
	if _, err := resolveWebSocketURL(ctx, "ws://example.test:9222/devtools/browser/x", nil, true); err != nil {
		t.Fatalf("an explicitly allowed remote endpoint was still refused: %v", err)
	}
}

func TestResolveWebSocketURLAcceptsSocketAndProbedEndpoints(t *testing.T) {
	ctx := context.Background()
	direct, err := resolveWebSocketURL(ctx, "ws://127.0.0.1:9222/devtools/browser/abc", nil, false)
	if err != nil || direct != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Fatalf("socket URL = %q, %v", direct, err)
	}
	var socket string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			t.Errorf("probed %s, want /json/version", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": socket})
	}))
	defer srv.Close()
	socket = "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools/browser/probed"

	got, err := resolveWebSocketURL(ctx, srv.URL, nil, false)
	if err != nil || got != socket {
		t.Fatalf("probed URL = %q, %v; want %q", got, err, socket)
	}
	// A bare host:port is the form a user is most likely to paste.
	if got, err = resolveWebSocketURL(ctx, strings.TrimPrefix(srv.URL, "http://"), nil, false); err != nil || got != socket {
		t.Fatalf("bare host URL = %q, %v; want %q", got, err, socket)
	}
}

func TestResolveWebSocketURLReportsAMissingEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer srv.Close()
	_, err := resolveWebSocketURL(context.Background(), srv.URL, nil, false)
	if err == nil || !strings.Contains(err.Error(), "remote-debugging-port") {
		t.Fatalf("got %v, want advice about --remote-debugging-port", err)
	}
}

func TestReadActivePortBuildsTheLoopbackSocketURL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, activePortFile), []byte("54321\n/devtools/browser/launched\n"), 0o600); err != nil {
		t.Fatalf("write port file: %v", err)
	}
	got, err := readActivePort(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("readActivePort: %v", err)
	}
	if got != "ws://127.0.0.1:54321/devtools/browser/launched" {
		t.Fatalf("socket URL = %q", got)
	}
}

func TestChromePathPrefersTheConfiguredBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chrome")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	got, err := chromePath(bin)
	if err != nil || got != bin {
		t.Fatalf("chromePath(configured) = %q, %v", got, err)
	}
	if _, err := chromePath(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a configured path that does not exist was accepted")
	}
}
