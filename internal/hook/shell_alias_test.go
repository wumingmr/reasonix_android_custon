package hook

import "testing"

func TestPwshMatchesLegacyShellHooks(t *testing.T) {
	for _, match := range []string{"bash", "Bash", "powershell", "PowerShell"} {
		native := ResolvedHook{HookConfig: HookConfig{Match: match}, Event: PreToolUse}
		if !MatchesTool(native, "pwsh") {
			t.Fatalf("native %s hook should match pwsh", match)
		}
	}
	claudeHook := ResolvedHook{HookConfig: HookConfig{Match: "Bash", PayloadFormat: "claude"}, Event: PreToolUse}
	if !MatchesTool(claudeHook, "pwsh") || claudeFacingToolName("pwsh") != "Bash" {
		t.Fatal("Claude Bash hook should receive pwsh as Bash")
	}
}
