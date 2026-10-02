package installsource

import (
	"strings"
	"testing"

	"reasonix/internal/config"
)

func TestPublicActionsOmitCredentialBearingState(t *testing.T) {
	public := publicActions([]action{{
		Kind: "mcp", Action: "install_mcp_server", Status: "planned",
		Env: map[string]string{"TOKEN": "env-secret"}, Headers: map[string]string{"Authorization": "header-secret"},
		entry: config.PluginEntry{
			Env:     map[string]string{"PRIVATE": "entry-env-secret"},
			Headers: map[string]string{"Authorization": "entry-header-secret"},
		},
	}})
	raw := marshalJSON(response{OK: true, Actions: public})
	for _, secret := range []string{
		"env-secret", "header-secret", "entry-env-secret", "entry-header-secret", "\"env\"", "\"headers\"",
	} {
		if strings.Contains(raw, secret) {
			t.Fatalf("public action contains credential-bearing data %q: %s", secret, raw)
		}
	}
}
