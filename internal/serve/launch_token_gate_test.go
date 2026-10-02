package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
)

func gateDo(t *testing.T, h http.Handler, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthDisabledGateHoldsEveryMutationSpelling(t *testing.T) {
	bc := NewBroadcaster()
	s := New(control.New(control.Options{Sink: bc}), bc, config.ServeConfig{AuthMode: "none"})
	h := s.Handler()
	tok := s.AuthToken()
	cases := []struct {
		name, method, target string
		hdr                  map[string]string
		want                 int
	}{
		{"PUT", http.MethodPut, "/approve", nil, 403},
		{"PATCH inbox", http.MethodPatch, "/inbox/items/x", nil, 403},
		{"DELETE inbox", http.MethodDelete, "/inbox/items/x", nil, 403},
		{"PROPFIND", "PROPFIND", "/approve", nil, 403},
		{"override header", http.MethodPost, "/approve", map[string]string{"X-HTTP-Method-Override": "GET"}, 403},
		{"dot path", http.MethodPost, "/./approve", nil, 403},
		{"double slash", http.MethodPost, "//approve", nil, 403},
		{"query token ignored", http.MethodPost, "/approve?token=" + tok, nil, 403},
		{"wrong bearer", http.MethodPost, "/approve", map[string]string{"Authorization": "Bearer x" + tok}, 403},
		{"basic scheme", http.MethodPost, "/approve", map[string]string{"Authorization": "Basic " + tok}, 403},
		{"wrong cookie", http.MethodPost, "/approve", map[string]string{"Cookie": cookieToken + "=nope"}, 403},
		{"lowercase bearer", http.MethodPost, "/approve", map[string]string{"Authorization": "bearer " + tok}, 400},
		{"cookie", http.MethodPost, "/approve", map[string]string{"Cookie": cookieToken + "=" + tok}, 400},
		{"OPTIONS passes gate", http.MethodOptions, "/approve", nil, 405},
		{"HEAD falls to GET / shell", http.MethodHead, "/approve", nil, 200},
	}
	for _, c := range cases {
		rec := gateDo(t, h, c.method, c.target, `{"allow":true}`, c.hdr)
		if rec.Code != c.want {
			t.Errorf("%s: %s %s = %d body=%q, want %d", c.name, c.method, c.target, rec.Code, strings.TrimSpace(rec.Body.String()), c.want)
		}
	}
	if rec := gateDo(t, h, http.MethodPost, "/auth/token", `{"token":""}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("/auth/token with empty token = %d, want 401", rec.Code)
	}
	if rec := gateDo(t, h, http.MethodGet, "/auth/token", "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /auth/token = %d, want 405", rec.Code)
	}
	rec := gateDo(t, h, http.MethodPost, "/auth/token", `{"token":"`+tok+`"}`, nil)
	sc := rec.Header().Get("Set-Cookie")
	if !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Lax") || !strings.Contains(sc, "Max-Age=") {
		t.Errorf("cookie flags = %q", sc)
	}
}

// A request to the DNS-rebinding host is refused by hostGuard before the
// token gate, even with no token.
func TestAuthDisabledHostAndCSRFGuardsAnswerFirst(t *testing.T) {
	bc := NewBroadcaster()
	s := New(control.New(control.Options{Sink: bc}), bc, config.ServeConfig{AuthMode: "none"})
	req := httptest.NewRequest(http.MethodPost, "/approve", strings.NewReader(`{}`))
	req.Host = "evil.example:8787"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding POST = %d, want 421", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/approve", strings.NewReader(`{}`))
	req.Host = "127.0.0.1"
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain POST = %d, want 415 from csrfGuard before token gate", rec.Code)
	}
}
