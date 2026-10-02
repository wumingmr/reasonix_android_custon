package main

import (
	"reasonix/internal/provider"
	"testing"
)

func TestHistoryPowerShellAliasesKeepArchivedCommand(t *testing.T) {
	for _, name := range []string{"bash", "BASH", "pwsh", "PWSH", "powershell", "PowerShell", "shell"} {
		msgs := []provider.Message{
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call", Name: name, Arguments: `{"command":"Write-Output example","description":"Display example"}`}}},
			{Role: provider.RoleTool, Name: name, ToolCallID: "call", Content: "example\n"},
		}
		history := historyMessages(msgs, func(s string) string { return s })
		call := history[0].ToolCalls[0]
		if !call.ArgumentsArchived || call.Subject != "Write-Output example" || call.Summary == "" {
			t.Fatalf("%s: %+v", name, call)
		}
		failed := historyMessages([]provider.Message{
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "failed", Name: name, Arguments: `{"command":"exit 7"}`}}},
			{Role: provider.RoleTool, Name: name, ToolCallID: "failed", Content: "error: command exited: exit status 7"},
		}, func(s string) string { return s })
		if got := failed[0].ToolCalls[0].Summary; got != "" {
			t.Fatalf("%s failed summary = %q, want empty", name, got)
		}
	}
}
