//go:build !windows

package builtin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"reasonix/internal/sandbox"
)

func gitMetadataWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws")
	if out, err := exec.Command("git", "init", "-q", ws).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return ws
}

func TestSandboxWriteHintAttributesGitMetadata(t *testing.T) {
	ws := gitMetadataWorkspace(t)
	spec := sandbox.Spec{Mode: "enforce", WriteRoots: []string{ws}}
	exit := errors.New("exit status 255")
	for _, out := range []string{
		"error: could not write config file .git/config: Operation not permitted",
		"cp: " + filepath.Join(ws, ".git", "hooks", "pre-commit") + ": Operation not permitted",
		"bash: .git/hooks/pre-push: Read-only file system",
	} {
		hint := appendSandboxWriteHint(out, exit, bashParams{Command: "x"}, spec, "", ws)
		if !strings.Contains(hint, sandbox.GitMetadataDeniedCode) || strings.Contains(hint, "Retry the same command with structured additional_write_dirs") {
			t.Fatalf("git metadata denial not attributed for %q:\n%s", out, hint)
		}
	}
	for _, out := range []string{
		"touch: /outside/file: Operation not permitted",
		"fatal: Unable to create '" + filepath.Join(ws, "main", ".git", "index.lock") + "': Operation not permitted",
		"error: could not write .git/config.lock-other: Operation not permitted",
	} {
		hint := appendSandboxWriteHint(out, exit, bashParams{Command: "x"}, spec, "", ws)
		if strings.Contains(hint, sandbox.GitMetadataDeniedCode) {
			t.Fatalf("unrelated denial attributed to git metadata for %q:\n%s", out, hint)
		}
	}
	exitZero := "error: could not write config file .git/config: Operation not permitted\nbranch 'topic' set up to track 'main'."
	if hint := appendSandboxWriteHint(exitZero, nil, bashParams{Command: "x"}, spec, "", ws); !strings.Contains(hint, sandbox.GitMetadataDeniedCode) {
		t.Fatalf("a refused write git reports with exit 0 is still attributed: %s", hint)
	}
	if hint := appendSandboxWriteHint("all good", nil, bashParams{Command: "x"}, spec, "", ws); hint != "all good" {
		t.Fatalf("an ordinary success carries no note: %s", hint)
	}
}

// The bash tool itself, under the real Seatbelt profile, tells the model the
// refused path is host-protected Git metadata.
func TestBashToolAttributesGitConfigDenial(t *testing.T) {
	if runtime.GOOS != "darwin" || !sandbox.Available() {
		t.Skip("needs the macOS Seatbelt backend")
	}
	ws := gitMetadataWorkspace(t)
	sh := sandbox.ResolveShell("", "", nil)
	tool := bash{sb: sandbox.Spec{Mode: "enforce", WriteRoots: []string{ws}}, shell: sh, workDir: ws}
	out, err := tool.Execute(context.Background(), argsJSON(t, map[string]any{"command": "git config user.name Someone"}))
	if err == nil {
		t.Fatalf("git config must be refused, got %q", out)
	}
	if !strings.Contains(out, sandbox.GitMetadataDeniedCode) || !strings.Contains(out, ".git/config") {
		t.Fatalf("denial not attributed to host-protected git metadata:\n%s", out)
	}
	data, readErr := os.ReadFile(filepath.Join(ws, ".git", "config"))
	if readErr != nil || strings.Contains(string(data), "Someone") {
		t.Fatalf("config changed: %v %s", readErr, data)
	}
}

func TestBashToolAttributesExitZeroConfigRefusal(t *testing.T) {
	if runtime.GOOS != "darwin" || !sandbox.Available() {
		t.Skip("needs the macOS Seatbelt backend")
	}
	ws := gitMetadataWorkspace(t)
	for _, args := range [][]string{{"-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "i"}, {"branch", "topic"}} {
		if out, err := exec.Command("git", append([]string{"-C", ws}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	tool := bash{sb: sandbox.Spec{Mode: "enforce", WriteRoots: []string{ws}}, shell: sandbox.ResolveShell("", "", nil), workDir: ws}
	out, err := tool.Execute(context.Background(), argsJSON(t, map[string]any{"command": "git branch --set-upstream-to=HEAD topic"}))
	if err != nil || !strings.Contains(out, sandbox.GitMetadataDeniedCode) {
		t.Fatalf("exit-0 refusal must be attributed: %v\n%s", err, out)
	}
}

func TestBashToolRefusesAHardLinkedGitConfig(t *testing.T) {
	if runtime.GOOS != "darwin" || !sandbox.Available() {
		t.Skip("needs the macOS Seatbelt backend")
	}
	ws := gitMetadataWorkspace(t)
	if err := os.Link(filepath.Join(ws, ".git", "config"), filepath.Join(ws, "cfg")); err != nil {
		t.Fatal(err)
	}
	tool := bash{sb: sandbox.Spec{Mode: "enforce", WriteRoots: []string{ws}}, shell: sandbox.ResolveShell("", "", nil), workDir: ws}
	out, err := tool.Execute(context.Background(), argsJSON(t, map[string]any{"command": "echo '[core]' >> cfg"}))
	if !errors.Is(err, sandbox.ErrGitMetadataLinked) {
		t.Fatalf("a hard-linked config must refuse the launch: %v %q", err, out)
	}
	if data, _ := os.ReadFile(filepath.Join(ws, ".git", "config")); strings.Contains(string(data), "[core]\n[core]") {
		t.Fatal("the command ran")
	}
}
