package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"reasonix/internal/persistentshell"
	"reasonix/internal/sandbox"
)

func TestBashLeasePathsAreProvenAndOneShot(t *testing.T) {
	root := t.TempDir()
	b := bash{workDir: root, shell: sandbox.Shell{Kind: sandbox.ShellBash, Path: "bash"}}
	for _, command := range []string{"echo hi > a.go", "printf '%s' hi >> a.go"} {
		args, _ := json.Marshal(map[string]string{"command": command})
		paths, ok := b.LeaseWritePaths(context.Background(), args)
		if !ok || len(paths) != 1 || paths[0] != filepath.Join(root, "a.go") {
			t.Fatalf("%q paths=%v proven=%v", command, paths, ok)
		}
	}
	for _, args := range []string{
		`{"command":"python edit.py"}`,
		`{"command":"echo hi > a.go","run_in_background":true}`,
		`{"command":"echo hi > a.go","preserve_background_processes":true}`,
		`{"command":"echo $(touch other) > a.go"}`,
		`{"command":"cd sub; echo hi > a.go"}`,
	} {
		if paths, ok := b.LeaseWritePaths(context.Background(), json.RawMessage(args)); ok {
			t.Fatalf("unproven write accepted: %s -> %v", args, paths)
		}
	}
	t.Setenv("BASH_ENV", "startup.sh")
	if _, ok := b.LeaseWritePaths(context.Background(), json.RawMessage(`{"command":"echo hi > a.go"}`)); ok {
		t.Fatal("shell startup code was treated as proven")
	}
	t.Setenv("BASH_ENV", "")
	b.shell.Kind = sandbox.ShellZsh
	if _, ok := b.LeaseWritePaths(context.Background(), json.RawMessage(`{"command":"echo hi > a.go"}`)); ok {
		t.Fatal("zsh startup files were treated as proven")
	}
	b.shell.Kind = sandbox.ShellBash
	b.persistent = &persistentshell.Manager{}
	if _, ok := b.LeaseWritePaths(context.Background(), json.RawMessage(`{"command":"echo hi > a.go"}`)); ok {
		t.Fatal("persistent cwd/function state was treated as proven")
	}
}
