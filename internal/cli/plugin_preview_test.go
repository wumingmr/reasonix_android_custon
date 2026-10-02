package cli

import (
	"encoding/json"
	"path/filepath"
	"reasonix/internal/pluginpkg"
	"strings"
	"testing"
)

func TestPluginDryRunPreservesApprovalPlan(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	source := filepath.Join(t.TempDir(), "demo")
	writePluginTestFile(t, filepath.Join(source, pluginpkg.CodexManifest), `{"name":"demo","version":"1.0.0","description":"Demo","skills":"skills"}`)
	writePluginTestFile(t, filepath.Join(source, "skills", "helper", "SKILL.md"), "---\nname: helper\ndescription: Help\n---\nHelp.")
	out := captureStdout(t, func() {
		if code := pluginCommand([]string{"install", source, "--dry-run"}); code != 0 {
			t.Errorf("exit %d", code)
		}
	})
	var result struct {
		Actions []json.RawMessage `json:"actions"`
		PlanID  string            `json:"planId"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) == 0 || !strings.HasPrefix(result.PlanID, "hmac-v1:") || len(result.PlanID) != 72 {
		t.Fatalf("dry-run lost reviewable plan: %s", out)
	}
}
