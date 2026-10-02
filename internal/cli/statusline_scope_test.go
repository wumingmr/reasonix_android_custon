package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/hook"
)

// A project's configuration cannot choose commands the TUI runs: the footer
// reads the statusline from config.Load, as the interactive session does.
func TestProjectStatuslineCommandNeverRunsFromTheTUI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("marker command is POSIX shell")
	}
	isolateUserConfig(t)
	repo := t.TempDir()
	marker := filepath.Join(repo, "statusline-ran")
	if err := os.WriteFile(filepath.Join(repo, "reasonix.toml"), []byte("[statusline]\ncommand = \"touch "+marker+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := newChatTUI(newOwnedTestController(t, control.Options{Label: "m"}), "", make(chan event.Event, 1), 80)
	m.statuslineCmd = cfg.Statusline.Command
	if cmd := m.runStatusline(); cmd != nil {
		cmd()
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("project statusline command %q ran", cfg.Statusline.Command)
	}
}

func TestStatuslineSpawnDisablesCwdExecutableSearch(t *testing.T) {
	var got hook.SpawnInput
	spawn := func(_ context.Context, in hook.SpawnInput) hook.SpawnResult {
		got = in
		return hook.SpawnResult{Stdout: "row\n"}
	}
	if out := runStatuslineCmdWith(spawn, "status", "{}", time.Second); out != "row" {
		t.Fatalf("statusline output = %q, want row", out)
	}
	if got.Env[hook.NoCwdCommandSearchEnv] != "1" {
		t.Fatalf("statusline spawned with env %v, want %s=1", got.Env, hook.NoCwdCommandSearchEnv)
	}
}
