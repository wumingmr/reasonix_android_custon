package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStepFunPresetsDeclareTheServedContextWindow(t *testing.T) {
	for _, id := range stepfunPresetIDs {
		preset, ok := CuratedProviderPreset(id)
		if !ok || len(preset.Entries) != 1 {
			t.Fatalf("missing %s preset", id)
		}
		if got := preset.Entries[0].ContextWindow; got != 262_144 {
			t.Fatalf("%s context_window = %d, want 262144 (0 turns automatic compaction off)", id, got)
		}
	}
}

func TestLoadForRootGivesInstalledStepFunPresetsTheirContextWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("REASONIX_CREDENTIALS_STORE", "file")
	body := `[[providers]]
name = "stepfun"
preset_id = "stepfun"
kind = "openai"
base_url = "https://api.stepfun.com/step_plan/v1"
models = ["step-3.7-flash", "step-3.5-flash", "step-3.5-flash-2603"]
default = "step-3.7-flash"

[[providers]]
name = "stepfun-anthropic"
preset_id = "stepfun-anthropic"
kind = "anthropic"
base_url = "https://api.stepfun.ai/step_plan/"
models = ["step-3.5-flash"]
default = "step-3.5-flash"

[[providers]]
name = "stepfun-api"
preset_id = "stepfun-api"
kind = "openai"
base_url = "https://api.stepfun.com/v1"
models = ["step-3.7-flash", "step-3.5-flash", "step-3.5-flash-2603"]
default = "step-3.7-flash"
context_window = 131072

[[providers]]
name = "stepfun-relay"
preset_id = "stepfun-responses"
kind = "responses"
base_url = "https://relay.example.com/v1"
models = ["step-3.7-flash"]
default = "step-3.7-flash"

[[providers]]
name = "stepfun-responses"
preset_id = "stepfun-responses"
kind = "responses"
base_url = "https://api.stepfun.com/v1"
models = ["step-3.7-flash", "private-model"]
default = "step-3.7-flash"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := LoadForRoot(t.TempDir())
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	for ref, want := range map[string]int{
		"stepfun/step-3.7-flash":           262_144,
		"stepfun-anthropic/step-3.5-flash": 262_144,
		"stepfun-api/step-3.7-flash":       131_072,
		"stepfun-relay/step-3.7-flash":     0,
		"stepfun-responses/step-3.7-flash": 0,
	} {
		entry, ok := c.ResolveModel(ref)
		if !ok {
			t.Fatalf("%s did not resolve", ref)
		}
		if entry.ContextWindow != want {
			t.Fatalf("%s context_window = %d, want %d", ref, entry.ContextWindow, want)
		}
	}
	edit := LoadForEdit(filepath.Join(home, "config.toml"))
	if got, ok := edit.Provider("stepfun"); !ok || got.ContextWindow != 262_144 {
		t.Fatalf("LoadForEdit stepfun = %+v, want context_window 262144", got)
	}
}
