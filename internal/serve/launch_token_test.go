package serve

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
)

var mutationRoutes = []string{
	"/submit",
	"/composer-profile",
	"/handoff",
	"/approve",
	"/plan-decision",
	"/answer",
	"/resolve-prompt",
	"/mcp-interaction",
	"/extension-form",
	"/tool-approval-mode",
	"/auto-approve-tools",
	"/bypass",
	"/permission/preset",
}

// operatorHandler stands in for the operator's frontend, which holds the
// launch token that mutations require.
func operatorHandler(s *Server) http.Handler {
	h := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+s.AuthToken())
		h.ServeHTTP(w, r)
	})
}

func postJSON(t *testing.T, url, body string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	maps.Copy(req.Header, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthDisabledRefusesMutationsWithoutLaunchToken(t *testing.T) {
	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc})
	srv := httptest.NewServer(New(ctrl, bc, config.ServeConfig{AuthMode: "none"}).Handler())
	defer srv.Close()

	for _, route := range mutationRoutes {
		resp := postJSON(t, srv.URL+route, `{"id":"1","allow":true,"session":true}`, nil)
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || body.Code != launchTokenRequiredCode {
			t.Errorf("POST %s without launch token = %d code=%q, want 403 %q", route, resp.StatusCode, body.Code, launchTokenRequiredCode)
		}
	}
	resp, err := http.Get(srv.URL + "/pending-prompts")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /pending-prompts with auth disabled = %d, want 200: reads stay open", resp.StatusCode)
	}
}

func TestAuthDisabledHonoursConfiguredToken(t *testing.T) {
	s := New(control.New(control.Options{Sink: NewBroadcaster()}), nil, config.ServeConfig{AuthMode: "none", Token: "proxy-held"})
	if s.AuthToken() != "proxy-held" {
		t.Fatalf("launch token = %q, want the configured token a fronting proxy injects", s.AuthToken())
	}
}

func TestAuthDisabledAcceptsHumanDecisionWithLaunchToken(t *testing.T) {
	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc})
	s := New(ctrl, bc, config.ServeConfig{AuthMode: "none"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	if s.AuthToken() == "" {
		t.Fatal("auth-disabled serve has no launch token")
	}

	resp := postJSON(t, srv.URL+"/approve", `{"allow":true}`, http.Header{"Authorization": {"Bearer " + s.AuthToken()}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /approve with bearer launch token = %d, want the handler's 400 for a missing id", resp.StatusCode)
	}

	resp = postJSON(t, srv.URL+"/auth/token", `{"token":"`+s.AuthToken()+`"}`, nil)
	resp.Body.Close()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == cookieToken {
			cookie = c
		}
	}
	if resp.StatusCode != http.StatusNoContent || cookie == nil {
		t.Fatalf("launch token bootstrap = %d cookie=%v, want 204 with %s", resp.StatusCode, cookie, cookieToken)
	}
	resp = postJSON(t, srv.URL+"/approve", `{"allow":true}`, http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /approve with launch token cookie = %d, want the handler's 400 for a missing id", resp.StatusCode)
	}

	resp = postJSON(t, srv.URL+"/approve", `{"allow":true}`, http.Header{"Authorization": {"Bearer wrong"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST /approve with a wrong token = %d, want 403", resp.StatusCode)
	}
}

func TestTokenModeAcceptsBearerLaunchToken(t *testing.T) {
	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc})
	s := New(ctrl, bc, config.ServeConfig{AuthMode: "token"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/approve", `{"id":"1","allow":true}`, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /approve in token mode = %d, want 401", resp.StatusCode)
	}
	resp = postJSON(t, srv.URL+"/approve", `{"allow":true}`, http.Header{"Authorization": {"Bearer " + s.AuthToken()}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /approve with bearer token = %d, want the handler's 400 for a missing id", resp.StatusCode)
	}
}
