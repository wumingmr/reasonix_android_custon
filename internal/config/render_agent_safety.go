package config

import (
	"fmt"
	"strings"
)

func renderAgentSafetyControls(b *strings.Builder, c *Config, scope RenderScope) {
	if len(c.Agent.PlanModeReadOnlyCommands) > 0 {
		fmt.Fprintf(b, "plan_mode_read_only_commands = %s   # legacy compatibility only; Plan bash uses Permissions\n", renderStringArray(c.Agent.PlanModeReadOnlyCommands))
	} else {
		b.WriteString("# plan_mode_read_only_commands = [\"gh issue view\"]   # legacy compatibility only; Plan bash uses Permissions\n")
	}
	_ = scope
}
