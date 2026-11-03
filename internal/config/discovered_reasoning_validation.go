package config

import (
	"slices"
	"strings"

	"reasonix/internal/provider"
)

func validDiscoveredReasoning(r *DiscoveredModelReasoning) bool {
	if r == nil || r.Invalid || len(r.Efforts) > 32 {
		return false
	}
	if r.Protocol != "" && normalizeReasoningProtocol(r.Protocol) != r.Protocol {
		return false
	}
	seen := map[string]bool{}
	for _, id := range r.Efforts {
		if id == "" || id == "auto" || id != strings.ToLower(strings.TrimSpace(id)) || seen[id] {
			return false
		}
		seen[id] = true
	}
	if r.Default != "" && len(r.Efforts) > 0 && !slices.Contains(r.Efforts, r.Default) {
		return false
	}
	if r.Supported != nil && !*r.Supported && len(r.Efforts) > 0 {
		return false
	}
	if r.Protocol == "none" && (len(r.Efforts) > 0 || r.Default != "" || (r.Supported != nil && *r.Supported)) {
		return false
	}
	return true
}

// DiscoveredReasoningUsable separates an endpoint declaration from a dialect
// the configured adapter can faithfully serialize. Manual settings still win.
func DiscoveredReasoningUsable(e *ProviderEntry, info *provider.ModelReasoningInfo) bool {
	if e == nil || info == nil {
		return false
	}
	r := &DiscoveredModelReasoning{Supported: info.Supported, Protocol: info.Protocol, Efforts: info.Efforts, Default: info.Default, Invalid: info.Invalid}
	if !validDiscoveredReasoning(r) {
		return false
	}
	protocol := explicitReasoningProtocol(e)
	if info.Protocol != "" {
		if protocol != "" && protocol != info.Protocol {
			return false
		}
		protocol = info.Protocol
	}
	if protocol == "" {
		return false
	}
	if len(info.Efforts) == 0 {
		return info.Protocol == "none"
	}
	cap := provider.ReasoningForConfig(e.Kind, provider.Config{BaseURL: e.BaseURL, Model: e.Model, Extra: map[string]any{"reasoning_protocol": protocol, "supported_efforts": info.Efforts, "default_effort": info.Default}})
	return cap.Validate(e.Model, "") == nil && slices.Equal(cap.IDs(), info.Efforts)
}
