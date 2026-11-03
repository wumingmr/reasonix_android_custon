package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"reasonix/internal/provider"
)

// Discovery is separate from manual overrides and bound to the serving route.
type DiscoveredModelReasoning struct {
	Supported *bool    `toml:"supported"`
	Protocol  string   `toml:"protocol"`
	Efforts   []string `toml:"efforts"`
	Default   string   `toml:"default"`
	Parameter bool     `toml:"parameter"`
	Invalid   bool     `toml:"invalid"`
	Identity  string   `toml:"identity"`
	ExpiresAt int64    `toml:"expires_at"`
}

func reasoningDiscoveryIdentity(e *ProviderEntry, model, revision string) string {
	data, _ := json.Marshal([]any{e.Name, e.Kind, e.BaseURL, e.RequestURL, e.ChatURL, e.ModelsURL, e.APIKeyEnv, e.Headers, e.NoProxy, e.AuthHeader, model, revision})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (e *ProviderEntry) ApplyDiscoveredReasoning(catalog []provider.ModelInfo) int {
	if e == nil {
		return 0
	}
	e.ModelOverrides = cloneModelOverrideMap(e.ModelOverrides)
	revision := CredentialStoreRevision()
	changed := 0
	for _, info := range catalog {
		if !e.HasModel(info.ID) {
			continue
		}
		ov := e.ModelOverrides[info.ID]
		if info.ReasoningInfo == nil {
			if ov.DiscoveredReasoning == nil {
				continue
			}
			ov.DiscoveredReasoning = nil
		} else {
			r := info.ReasoningInfo
			ov.DiscoveredReasoning = &DiscoveredModelReasoning{Supported: r.Supported, Protocol: r.Protocol, Efforts: slices.Clone(r.Efforts), Default: r.Default, Parameter: r.Parameter, Invalid: r.Invalid,
				Identity: reasoningDiscoveryIdentity(e, info.ID, revision), ExpiresAt: time.Now().Add(24 * time.Hour).Unix()}
		}
		if e.ModelOverrides == nil {
			e.ModelOverrides = map[string]ProviderModelOverride{}
		}
		if modelOverrideEmpty(ov) {
			delete(e.ModelOverrides, info.ID)
		} else {
			e.ModelOverrides[info.ID] = ov
		}
		changed++
	}
	return changed
}

func (e *ProviderEntry) discoveredReasoning() *DiscoveredModelReasoning {
	ov, ok := e.ModelOverrides[e.Model]
	if !ok || ov.DiscoveredReasoning == nil {
		return nil
	}
	r := ov.DiscoveredReasoning
	if r.Identity != reasoningDiscoveryIdentity(e, e.Model, CredentialStoreRevision()) || r.ExpiresAt <= time.Now().Unix() {
		return nil
	}
	return r
}

func applyDiscoveredReasoning(e *ProviderEntry) {
	r := e.discoveredReasoning()
	if !validDiscoveredReasoning(r) {
		return
	}
	// Bare capability booleans/parameter names never choose a dialect or levels.
	protocol := explicitReasoningProtocol(e)
	if r.Protocol == "" {
		if protocol == "" || len(r.Efforts) == 0 {
			return
		}
	} else if normalizeReasoningProtocol(r.Protocol) != r.Protocol || (protocol != "" && protocol != r.Protocol) {
		return
	}
	if !DiscoveredReasoningUsable(e, &provider.ModelReasoningInfo{Supported: r.Supported, Protocol: r.Protocol, Efforts: r.Efforts, Default: r.Default, Invalid: r.Invalid}) {
		return
	}
	if explicitReasoningProtocol(e) == "" && r.Protocol != "" {
		e.ReasoningProtocol = r.Protocol
		e.discoveredProtocol = true
	}
	if len(e.SupportedEfforts) == 0 && len(r.Efforts) > 0 {
		e.SupportedEfforts = slices.Clone(r.Efforts)
		e.discoveredEfforts = true
	}
	if e.discoveredEfforts && e.DefaultEffort == "" && r.Default != "" && slices.Contains(e.SupportedEfforts, r.Default) {
		e.DefaultEffort = r.Default
		e.discoveredDefault = true
	}
}

func stripDiscoveredReasoning(e *ProviderEntry) {
	if !e.discoveredProtocol && !e.discoveredEfforts && !e.discoveredDefault {
		return
	}
	if ov, ok := e.ModelOverrides[e.Model]; ok && ov.DiscoveredReasoning != nil {
		r := ov.DiscoveredReasoning
		if e.discoveredProtocol && e.ReasoningProtocol == r.Protocol {
			e.ReasoningProtocol = ""
		}
		if e.discoveredEfforts && slices.Equal(e.SupportedEfforts, r.Efforts) {
			e.SupportedEfforts = nil
		}
		if e.discoveredDefault && e.DefaultEffort == r.Default {
			e.DefaultEffort = ""
		}
	}
	e.discoveredProtocol, e.discoveredEfforts, e.discoveredDefault = false, false, false
}

func renderDiscoveredReasoning(r DiscoveredModelReasoning) string {
	parts := []string{fmt.Sprintf("identity = %q", r.Identity), fmt.Sprintf("expires_at = %d", r.ExpiresAt)}
	if r.Supported != nil {
		parts = append(parts, fmt.Sprintf("supported = %t", *r.Supported))
	}
	if r.Protocol != "" {
		parts = append(parts, fmt.Sprintf("protocol = %q", r.Protocol))
	}
	if len(r.Efforts) > 0 {
		parts = append(parts, "efforts = "+renderStringArray(r.Efforts))
	}
	if r.Default != "" {
		parts = append(parts, fmt.Sprintf("default = %q", r.Default))
	}
	if r.Parameter {
		parts = append(parts, "parameter = true")
	}
	if r.Invalid {
		parts = append(parts, "invalid = true")
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
