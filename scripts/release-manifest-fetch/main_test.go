package main

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestFetchManifestUsesUpdaterClientAndRejectsChallenges(t *testing.T) {
	for _, test := range []struct {
		name, body, mitigation string
		status                 int
		wantError              string
	}{
		{name: "public manifest", body: `{"version":"v1.38.11"}`, status: http.StatusOK},
		{name: "challenge", body: "<html>challenge</html>", status: http.StatusForbidden, mitigation: "challenge", wantError: "HTTP 403"},
		{name: "malformed JSON", body: "<html>challenge</html>", status: http.StatusOK, wantError: "not JSON"},
		{name: "invalid version", body: `{"version":"preview"}`, status: http.StatusOK, wantError: "valid Stable version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				wantAgent := "Reasonix-Updater/v1.38.12 (" + runtime.GOOS + "/" + runtime.GOARCH + "; build=stable; update=stable)"
				if got := request.Header.Get("User-Agent"); got != wantAgent {
					t.Errorf("User-Agent = %q, want %q", got, wantAgent)
				}
				writer.Header().Set("cf-mitigated", test.mitigation)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			body, err := fetchManifest(server.Client(), server.URL, "1.38.12")
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				if len(body) != 0 {
					t.Fatalf("failed response returned %d bytes", len(body))
				}
				return
			}
			if err != nil || string(body) != test.body {
				t.Fatalf("body = %q, error = %v", body, err)
			}
		})
	}
}
