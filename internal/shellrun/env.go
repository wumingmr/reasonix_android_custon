package shellrun

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"reasonix/internal/proc"
	"reasonix/internal/secrets"
)

// LoginEnvironment merges the user's login-shell PATH into a desktop process
// environment, where shell startup files are not otherwise loaded.
var loginPathCache sync.Map

func LoginShellPATH(ctx context.Context, env []string, shell string) string {
	if v, ok := loginPathCache.Load(shell); ok {
		return v.(string)
	}
	path := probeLoginShellPATH(ctx, env, shell)
	loginPathCache.Store(shell, path)
	return path
}

func LoginEnvironment(ctx context.Context, env []string, shell string) []string {
	if runtime.GOOS == "windows" || strings.TrimSpace(shell) == "" {
		return env
	}
	current := envValue(env, "PATH")
	if current == "" {
		return env
	}
	login := LoginShellPATH(ctx, env, shell)
	if login == "" || login == current {
		return env
	}
	return setEnvValue(env, "PATH", mergePath(login, current))
}

func probeLoginShellPATH(parent context.Context, env []string, shell string) string {
	if runtime.GOOS == "windows" || strings.TrimSpace(shell) == "" {
		return ""
	}
	const marker = "__REASONIX_LOGIN_PATH__="
	for _, args := range [][]string{{"-l", "-i", "-c"}, {"-l", "-c"}, {"-c"}} {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		cmd := proc.CommandContext(ctx, shell, append(args, "printf '\\n"+marker+"%s\\n' \"$PATH\"")...)
		cmd.Env = secrets.ProcessEnv()
		proc.PrepareShellPATHProbe(cmd)
		cmd.Stdin = strings.NewReader("")
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
			if rest, ok := strings.CutPrefix(line, marker); ok && strings.TrimSpace(rest) != "" {
				return strings.TrimSpace(rest)
			}
		}
	}
	return ""
}

func envValue(env []string, key string) string {
	for _, item := range env {
		if k, v, ok := strings.Cut(item, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func setEnvValue(env []string, key, value string) []string {
	out := append([]string(nil), env...)
	for i, item := range out {
		if strings.HasPrefix(item, key+"=") {
			out[i] = key + "=" + value
			return out
		}
	}
	return append(out, key+"="+value)
}

func mergePath(login, current string) string {
	seen := map[string]bool{}
	var parts []string
	for _, list := range []string{login, current} {
		for part := range strings.SplitSeq(list, string(os.PathListSeparator)) {
			if part != "" && !seen[part] {
				seen[part] = true
				parts = append(parts, part)
			}
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}
