package main

import (
	"fmt"
	"strings"

	"reasonix/internal/permission"
	"reasonix/internal/plugin"
	"reasonix/internal/tool"
)

func validateSavedPermissionRule(list, raw string, tools []tool.ContractEntry, servers []string) error {
	rule, ok := permission.ParseRule(raw)
	if !ok {
		return nil // AddPermissionRule reports malformed rules.
	}
	for _, candidate := range tools {
		if permission.RuleMatchesString(rule.Tool, candidate.Name, "") {
			return nil
		}
	}
	for _, server := range servers {
		prefix := plugin.ToolPrefix(server)
		suffix := strings.TrimPrefix(rule.Tool, prefix)
		if (suffix != rule.Tool && validConfiguredMCPToolSuffix(suffix)) || rule.Tool == plugin.MCPConnectPermissionName(server) {
			return nil
		}
	}
	if strings.HasPrefix(rule.Tool, "mcp__") && strings.ContainsAny(rule.Tool, "*?") {
		return fmt.Errorf("permission rule %q uses an MCP tool-name wildcard; MCP permission rules require an exact tool name", raw)
	}
	if rule.Subject == "" && !strings.ContainsAny(rule.Tool, "()") {
		// Allow uses an exact reusable command; ask/deny use a command prefix
		// so arguments to the same command remain covered.
		if strings.EqualFold(strings.TrimSpace(list), "allow") {
			return fmt.Errorf("permission rule %q names no registered tool; if this is a shell command, use Bash(%s)", raw, rule.Tool)
		}
		return fmt.Errorf("permission rule %q names no registered tool; if this is a shell command, use Bash(%s:*)", raw, rule.Tool)
	}
	return fmt.Errorf("permission rule %q names no registered tool", raw)
}

func validConfiguredMCPToolSuffix(suffix string) bool {
	if suffix == "" {
		return false
	}
	for _, c := range suffix {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
