package config

import (
	"slices"
	"strings"
)

// stepfunContextWindow is the limit StepFun serves for every model its presets
// list; the API refuses a request above 262144 tokens.
const stepfunContextWindow = 262_144

var stepfunPresetIDs = []string{"stepfun", "stepfun-responses", "stepfun-anthropic", "stepfun-api", "stepfun-api-anthropic"}

// normalizeLegacyStepFunContextWindows gives an installed StepFun preset that
// still has no window the one its preset declares. A zero window turns
// automatic compaction off, so the session grows until the API refuses it.
// Either official host counts, since the region is the user's; a custom
// endpoint, a model StepFun does not serve, or any window already set stays.
func normalizeLegacyStepFunContextWindows(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		presetID := strings.TrimSpace(p.PresetID)
		if p.ContextWindow != 0 || !slices.Contains(stepfunPresetIDs, presetID) {
			continue
		}
		preset, ok := CuratedProviderPreset(presetID)
		if !ok || len(preset.Entries) != 1 {
			continue
		}
		canonical := preset.Entries[0]
		if canonical.ContextWindow <= 0 ||
			!strings.EqualFold(strings.TrimSpace(p.Kind), strings.TrimSpace(canonical.Kind)) ||
			!stepfunOfficialBaseURL(p.BaseURL, canonical.BaseURL) ||
			!stepfunServesAll(p.ModelList(), canonical.Models) {
			continue
		}
		p.ContextWindow = canonical.ContextWindow
		changed = true
	}
	return changed
}

func stepfunOfficialBaseURL(raw, canonical string) bool {
	got := normalizedBaseURLForMigration(raw)
	want := normalizedBaseURLForMigration(canonical)
	return got == want || got == strings.Replace(want, "://api.stepfun.com", "://api.stepfun.ai", 1)
}

func stepfunServesAll(models, served []string) bool {
	if len(models) == 0 {
		return false
	}
	for _, m := range models {
		if !slices.Contains(served, strings.TrimSpace(m)) {
			return false
		}
	}
	return true
}
