package hook

import (
	"regexp"
	"slices"
)

// MatchesTool reports whether a hook applies to toolName. The match field is an
// anchored regex; non-tool events always match. A malformed regex never fires.
func MatchesTool(h ResolvedHook, toolName string) bool {
	if !UsesToolMatcher(h.Event) {
		return true
	}
	m := h.Match
	if m == "" || m == "*" {
		return true
	}
	re, err := regexp.Compile("^(?:" + m + ")$")
	if err != nil {
		return false
	}
	if h.PayloadFormat != "claude" {
		if re.MatchString(toolName) {
			return true
		}
		if toolName == "pwsh" {
			return re.MatchString("bash") || re.MatchString("Bash") ||
				re.MatchString("powershell") || re.MatchString("PowerShell")
		}
		return false
	}
	return slices.ContainsFunc(claudeMatchNames(toolName), re.MatchString)
}
