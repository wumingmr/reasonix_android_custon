package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/sandbox"
	"reasonix/internal/shellsafe"
)

// LeaseWritePaths proves a small one-shot shell's targets. Persistent shells
// and startup code stay opaque; model-supplied write dirs are not evidence.
func (b bash) LeaseWritePaths(ctx context.Context, args json.RawMessage) ([]string, bool) {
	var p bashParams
	kind := b.resolved().Kind
	if json.Unmarshal(args, &p) != nil || (kind != sandbox.ShellBash && kind != sandbox.ShellSh) ||
		b.persistentManager(ctx) != nil || b.terminal != nil || hasShellStartupCode() ||
		p.RunInBackground || p.PreserveBackgroundProcesses {
		return nil, false
	}
	paths, ok := shellsafe.StaticWritePaths(p.Command)
	if !ok {
		return nil, false
	}
	for i, path := range paths {
		if !filepath.IsAbs(path) {
			paths[i] = resolveIn(b.workDir, path)
		}
	}
	return paths, true
}

func hasShellStartupCode() bool {
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if (key == "BASH_ENV" || key == "ENV") && value != "" {
			return true
		}
		if strings.HasPrefix(key, "BASH_FUNC_") {
			return true
		}
	}
	return false
}
