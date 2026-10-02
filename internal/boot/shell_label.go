package boot

import (
	"path/filepath"
	"strings"

	"reasonix/internal/sandbox"
)

// resolvedShellLabel preserves the cache-stable kind label when the user did
// not configure an explicit path. When a path was configured, it reports the
// interpreter actually bound after validation and fallback, so a stale path
// can never describe a different executable.
func resolvedShellLabel(shell sandbox.Shell, configuredPath, userShellPath string) string {
	label := shell.Kind.String()
	if strings.TrimSpace(configuredPath) != "" {
		if path := strings.TrimSpace(shell.Path); path != "" {
			label = path
		}
	}
	userShell := filepath.Base(strings.TrimSpace(userShellPath))
	if userShell != "." && userShell != "" && !strings.ContainsAny(userShell, "\r\n") && userShell != filepath.Base(label) {
		return label + " (tool subprocess; user login shell: " + userShell + ")"
	}
	return label
}
