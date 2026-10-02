package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestScopedRenderSeparatesUserAndProjectConfig(t *testing.T) {
	c := Default()
	c.Language = "zh"
	c.Desktop.Language = "zh"
	c.Desktop.Currency = "CNY"
	c.Desktop.Theme = "dark"
	c.Desktop.ThemeStyle = "graphite"
	c.Desktop.CloseBehavior = "background"
	c.Desktop.StatusBarStyle = "text"
	c.Desktop.StatusBarStyleInitialized = true
	c.Desktop.DefaultToolApprovalMode = "workspace-write"
	c.Desktop.CheckUpdates = boolPtr(false)
	c.Desktop.UpdateChannel = "preview"
	c.Agent.RecoveryModel = "deepseek-pro"
	c.Agent.RecoveryTemperature = 0.2

	user := RenderTOMLForScope(c, RenderScopeUser)
	for _, want := range []string{fmt.Sprintf("config_version = %d", Default().ConfigVersion), "[desktop]", `currency = "CNY"`, "[billing]", `display_currency = "CNY"`, `theme = "dark"`, `terminal_theme = "auto"`, `close_behavior = "background"`, `status_bar_style = "text"`, `default_tool_approval_mode = "workspace-write"`, `check_updates = false`, "[notifications]", "[tools.shell]"} {
		if !strings.Contains(user, want) {
			t.Fatalf("user render missing %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "update_channel") || strings.Contains(user, "[cli]") {
		t.Fatalf("user render retained retired update channel:\n%s", user)
	}

	project := RenderTOMLForScope(c, RenderScopeProject)
	for _, forbidden := range []string{"[desktop]", "[notifications]", "close_behavior =", "default_tool_approval_mode =", "default_auto_recovery_checkpoint =", "check_updates =", "update_channel =", "max_steps", "planner_max_steps"} {
		if strings.Contains(project, forbidden) {
			t.Fatalf("project render should not contain %q:\n%s", forbidden, project)
		}
	}
	for _, retired := range []string{"default_auto_recovery_checkpoint", "auto_recovery_checkpoint"} {
		if strings.Contains(user, retired) || strings.Contains(project, retired) {
			t.Fatalf("retired Auto Guard key %q must not be rendered:\nuser:\n%s\nproject:\n%s", retired, user, project)
		}
	}
	if strings.Contains(project, "\nsystem_prompt = \"\"\"") {
		t.Fatalf("project render should not pin the built-in system prompt:\n%s", project)
	}
	if !strings.Contains(project, "# system_prompt =") {
		t.Fatalf("project render should leave a system prompt hint:\n%s", project)
	}
	if strings.Contains(user, "recovery_model") || strings.Contains(project, "recovery_model") {
		t.Fatalf("retired recovery_model rendered:\nuser:\n%s\nproject:\n%s", user, project)
	}
	if strings.Contains(user, "auto_plan") || strings.Contains(project, "auto_plan") {
		t.Fatalf("retired auto-plan keys must not be rendered:\nuser:\n%s\nproject:\n%s", user, project)
	}
	if strings.Contains(user, "recovery_temperature") || strings.Contains(project, "recovery_temperature") {
		t.Fatalf("deprecated recovery_temperature must not be rendered:\nuser:\n%s\nproject:\n%s", user, project)
	}
}
