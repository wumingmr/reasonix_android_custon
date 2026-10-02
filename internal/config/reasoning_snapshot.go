package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"strings"
)

func reasoningSnapshotHash(ov ProviderModelOverride) string {
	data, _ := json.Marshal(struct {
		Protocol string
		Efforts  []string
		Default  string
	}{ov.ReasoningProtocol, ov.SupportedEfforts, ov.DefaultEffort})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func reasoningCompatibilitySnapshots(entries []ProviderEntry) []ProviderEntry {
	snapshots := make([]ProviderEntry, len(entries))
	for i, entry := range entries {
		snapshots[i] = reasoningCompatibilitySnapshot(entry)
	}
	return snapshots
}

// explicitModelReasoning peels only untouched generated fields. Legacy writers
// that change a declaration invalidate the digest, preserving the entire edit.
// A writer that drops the optional marker also leaves explicit declarations.
func explicitModelReasoning(ov ProviderModelOverride) ProviderModelOverride {
	mask, digest, ok := strings.Cut(ov.ReasoningDefaults, ":")
	ov.ReasoningDefaults = ""
	if !ok || digest != reasoningSnapshotHash(ov) || strings.Trim(mask, "psd") != "" {
		return ov
	}
	if strings.Contains(mask, "p") {
		ov.ReasoningProtocol = ""
	}
	if strings.Contains(mask, "s") {
		ov.SupportedEfforts = nil
	}
	if strings.Contains(mask, "d") {
		ov.DefaultEffort = ""
	}
	return ov
}

// reasoningCompatibilitySnapshot keeps older readers functional without making
// generated defaults permanent user overrides in current readers. This copy is
// used only at serialization; runtime and settings retain explicit fields only.
func reasoningCompatibilitySnapshot(entry ProviderEntry) ProviderEntry {
	e := entry
	stripRuntimeReasoningDefaults(&e)
	e.ModelOverrides = maps.Clone(entry.ModelOverrides)
	for _, model := range e.ModelList() {
		ov := explicitModelReasoning(e.ModelOverrides[model])
		selected := e
		selected.Model = model
		resolved := ResolveReasoningEntry(&selected)
		mask := ""
		if ov.ReasoningProtocol == "" && explicitReasoningProtocol(&e) == "" && resolved.ReasoningProtocol != "" {
			ov.ReasoningProtocol = resolved.ReasoningProtocol
			mask += "p"
		}
		if len(ov.SupportedEfforts) == 0 && len(e.SupportedEfforts) == 0 && len(resolved.SupportedEfforts) > 0 {
			ov.SupportedEfforts = append([]string(nil), resolved.SupportedEfforts...)
			mask += "s"
		}
		if ov.DefaultEffort == "" && e.DefaultEffort == "" && resolved.DefaultEffort != "" {
			ov.DefaultEffort = resolved.DefaultEffort
			mask += "d"
		}
		if mask == "" {
			continue
		}
		ov.ReasoningDefaults = mask + ":" + reasoningSnapshotHash(ov)
		if e.ModelOverrides == nil {
			e.ModelOverrides = map[string]ProviderModelOverride{}
		}
		e.ModelOverrides[model] = ov
	}
	return e
}
