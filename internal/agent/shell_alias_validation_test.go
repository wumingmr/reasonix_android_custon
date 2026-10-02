package agent

import (
	"encoding/json"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
	"reasonix/internal/tool/builtin"
)

func TestLegacyShellAliasesPassArgumentGateWithoutDescription(t *testing.T) {
	r := tool.NewRegistry()
	r.Add(builtin.ConfineBash(sandbox.Spec{Mode: "enforce", Shell: sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: "pwsh"}}, builtin.SessionDataGuard{}))
	for _, name := range []string{"bash", "Bash", "PowerShell", "powershell", "Pwsh", "pwsh"} {
		t.Run(name, func(t *testing.T) {
			target, _, _ := r.ResolveCall(name)
			plan := &toolCallPlan{call: provider.ToolCall{Name: name}, execTool: target, permName: "pwsh", execArgs: json.RawMessage(`{"command":"Write-Output legacy"}`)}
			_, blocked := (&Agent{}).applyArgumentValidation(plan)
			if blocked != (name == "pwsh") {
				t.Fatalf("blocked=%v for %s", blocked, name)
			}
			if name != "pwsh" {
				var args map[string]string
				_ = json.Unmarshal(plan.execArgs, &args)
				if args["command"] != "Write-Output legacy" || args["description"] == "" {
					t.Fatalf("args=%s", plan.execArgs)
				}
				if string(plan.execArgs) != string(plan.permArgs) {
					t.Fatal("permission args diverged")
				}
			}
		})
	}
	// Compatibility must not coerce malformed values or drop security arguments.
	target, _, _ := r.ResolveCall("bash")
	for _, raw := range []string{`{"command":42}`, `{"command":"x","description":42}`, `{"command":"x","sandbox_permissions":"invalid"}`} {
		plan := &toolCallPlan{call: provider.ToolCall{Name: "bash"}, execTool: target, permName: "pwsh", execArgs: json.RawMessage(raw)}
		if _, blocked := (&Agent{}).applyArgumentValidation(plan); !blocked {
			t.Fatalf("invalid args accepted: %s", raw)
		}
	}
}
