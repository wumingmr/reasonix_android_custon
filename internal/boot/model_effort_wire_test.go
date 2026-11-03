package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

func TestModelEffortAndSessionOverrideReachWire(t *testing.T) {
	isolateConfigHome(t)
	observed := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model  string `json:"model"`
			Effort string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		observed <- body.Model + ":" + body.Effort
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{{Name: "relay", Kind: "openai", BaseURL: server.URL, Models: []string{"a", "b"}, Effort: "high", ReasoningProtocol: "openai", SupportedEfforts: []string{"low", "high"}, DefaultEffort: "low"}}
	low := "low"
	if err := cfg.SetModelEffort("relay", "a", &low); err != nil {
		t.Fatal(err)
	}
	resolver := NewLocalProviderResolverWithCapabilities(cfg, netclient.ProxySpec{}, config.NewTransientModelCapabilityResolver())
	high := "high"
	for _, tc := range []struct {
		selection provider.Selection
		want      string
	}{
		{provider.Selection{Ref: "relay/a"}, "a:low"},
		{provider.Selection{Ref: "relay/b"}, "b:high"},
		{provider.Selection{Ref: "relay/a", Effort: &high}, "a:high"},
	} {
		p, err := resolver.Resolve(tc.selection)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "ok"}}})
		if err != nil {
			t.Fatal(err)
		}
		for chunk := range stream {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
		}
		if got := <-observed; got != tc.want {
			t.Fatalf("wire=%s want=%s", got, tc.want)
		}
	}
}
