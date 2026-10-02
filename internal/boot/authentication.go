package boot

import (
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
)

func authenticationStateForModelEntry(entry *config.ProviderEntry, modelRef string) control.AuthenticationState {
	if entry == nil || !entry.RequiresAPIKey() || entry.APIKey() != "" {
		return control.AuthenticationState{Status: control.AuthenticationReady}
	}
	status := control.AuthenticationMissingCredential
	code := "missing_credential"
	switch config.CredentialStoreRevision() {
	case "unavailable", "unreadable":
		status = control.AuthenticationCredentialStoreUnavailable
		code = "credential_store_unavailable"
	}
	return control.AuthenticationState{
		Status:       status,
		ProviderName: entry.Name,
		ModelRef:     modelRef,
		KeyEnv:       entry.APIKeyEnv,
		Code:         code,
	}
}

// Capture absence as well as presence once. A request must never reread keys
// changed by another process in the middle of the runtime's current turn.
func authenticationReader(cfg *config.Config, external provider.Resolver) func(string) control.AuthenticationState {
	states := map[string]control.AuthenticationState{}
	if external == nil {
		for i := range cfg.Providers {
			entry := &cfg.Providers[i]
			states[entry.Name] = authenticationStateForModelEntry(entry, "")
		}
	}
	return func(ref string) control.AuthenticationState {
		name, _, _ := strings.Cut(ref, "/")
		state := states[name]
		if state.Status == "" {
			state.Status = control.AuthenticationReady
		}
		state.ModelRef = ref
		return state
	}
}

func runtimeImageEnabled(p provider.Provider, fallback bool) bool {
	if info, ok := p.(provider.ModelInfoProvider); ok {
		return info.ModelInfo().SupportsInput(provider.ModalityImage)
	}
	return fallback
}
