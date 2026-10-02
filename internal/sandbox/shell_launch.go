package sandbox

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"reasonix/internal/proc"
	"reasonix/internal/secrets"
)

// powerShellLaunches reports whether a found PowerShell can start. A Store
// install is an app execution alias: a zero-byte reparse point (irregular to
// os.Lstat) that stats fine yet can fail to start the packaged app for this
// token. Only such a path pays a launch; a regular file is taken as found.
func powerShellLaunches(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeIrregular == 0 {
		return true
	}
	return aliasLaunches.check(p)
}

// launchablePowerShellLookPath hides a PowerShell on PATH that cannot start, so
// discovery falls through to the next interpreter instead of selecting it.
func launchablePowerShellLookPath(lookPath func(string) (string, error), launches func(string) bool) func(string) (string, error) {
	return func(name string) (string, error) {
		p, err := lookPath(name)
		if err == nil && isPowerShellName(name) && !launches(p) {
			return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return p, err
	}
}

func launchablePowerShells(candidates []string, launches func(string) bool) []string {
	out := make([]string, 0, len(candidates))
	for _, p := range candidates {
		if launches(p) {
			out = append(out, p)
		}
	}
	return out
}

func isPowerShellName(name string) bool {
	base := strings.TrimSuffix(strings.ToLower(pathBase(name)), ".exe")
	return base == "pwsh" || base == "powershell"
}

// launchProbeTTL bounds how long one alias probe answers the several discovery
// passes a session build makes.
const launchProbeTTL = 30 * time.Second

type launchProbe struct {
	at time.Time
	ok bool
}

type launchProbes struct {
	mu   sync.Mutex
	seen map[string]launchProbe
	run  func(string) bool
}

var aliasLaunches = &launchProbes{seen: map[string]launchProbe{}, run: probePowerShell}

func (l *launchProbes) check(p string) bool {
	key := strings.ToLower(p)
	l.mu.Lock()
	r, hit := l.seen[key]
	l.mu.Unlock()
	if hit && time.Since(r.at) < launchProbeTTL {
		return r.ok
	}
	ok := l.run(p)
	l.mu.Lock()
	l.seen[key] = launchProbe{at: time.Now(), ok: ok}
	l.mu.Unlock()
	return ok
}

func (l *launchProbes) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	clear(l.seen)
}

func probePowerShell(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return powerShellProbeCommand(ctx, path).Run() == nil
}

// powerShellProbeCommand starts nothing of the user's: no profile, a scrubbed
// environment, and a working directory outside the workspace.
func powerShellProbeCommand(ctx context.Context, path string) *exec.Cmd {
	cmd := proc.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "exit 0")
	cmd.Env = secrets.ProcessEnv()
	cmd.Dir = os.TempDir()
	proc.HideWindow(cmd)
	return cmd
}
