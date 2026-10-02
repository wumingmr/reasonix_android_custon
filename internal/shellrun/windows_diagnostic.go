package shellrun

import "strings"

// WindowsRuntimeDiagnostic recognizes stderr evidence, never a bare exit code.
// The failure may belong to an external child after earlier commands ran, so
// callers must not relabel an ordinary execution failure as mutation-not-started.
func WindowsRuntimeDiagnostic(output string) string {
	text := strings.ToLower(output)
	if !strings.Contains(text, "fatal error") && !strings.Contains(text, "cygheap_user::init") {
		return ""
	}
	if (strings.Contains(text, "createfilemapping") && strings.Contains(text, "win32 error 5")) ||
		(strings.Contains(text, "cygheap_user::init") && strings.Contains(text, "ntsetinformationtoken") && strings.Contains(text, "0xc0000022")) {
		return "Windows denied MSYS/Cygwin runtime initialization (access denied). Check compatibility with the current sandbox and token permissions; repeating commands through the same failing runtime will not repair it. Sandbox restrictions remain in effect"
	}
	return ""
}
