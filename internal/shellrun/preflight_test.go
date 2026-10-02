package shellrun

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"testing"

	"reasonix/internal/proc"
	"reasonix/internal/tool"
)

func TestRunForegroundExecutesRequestedCommandOnce(t *testing.T) {
	calls := 0
	res := RunForeground(context.Background(), Request{
		Argv: []string{"real-command", "arg"},
		Run: func(_ context.Context, cmd *exec.Cmd, _ proc.RunOptions) (*proc.TrackedCommand, error) {
			calls++
			if got := cmd.Args; len(got) != 2 || got[0] != "real-command" || got[1] != "arg" {
				t.Fatalf("command = %v", got)
			}
			fmt.Fprint(cmd.Stdout, "ok")
			return nil, nil
		},
	})
	if calls != 1 || res.State != tool.ShellStateCompleted || res.Combined != "ok" {
		t.Fatalf("calls=%d result=%+v", calls, res)
	}
}

func TestOrdinaryExit126RemainsExecutionFailure(t *testing.T) {
	res := RunForeground(context.Background(), Request{
		Argv: []string{"pwsh"},
		Run: func(_ context.Context, cmd *exec.Cmd, _ proc.RunOptions) (*proc.TrackedCommand, error) {
			var child *exec.Cmd
			if runtime.GOOS == "windows" {
				child = exec.Command("cmd", "/c", "exit", "126")
			} else {
				child = exec.Command("sh", "-c", "exit 126")
			}
			err := child.Run()
			cmd.Process = child.Process
			cmd.ProcessState = child.ProcessState
			return nil, err
		},
	})
	if !res.Started || res.FailurePhase != tool.ShellPhaseExecution || res.ExitCode == nil || *res.ExitCode != 126 {
		t.Fatalf("ordinary exit 126 misclassified: %+v", res)
	}
}

func TestWindowsRuntimeDiagnosticsRequireEvidence(t *testing.T) {
	for _, text := range []string{"exit status 256", "access denied", "CreateFileMapping failed", "Win32 error 5"} {
		if WindowsRuntimeDiagnostic(text) != "" {
			t.Fatalf("misclassified %q", text)
		}
	}
	for _, text := range []string{
		"*** fatal error - CreateFileMapping S-1-5-21-1.1, Win32 error 5. Terminating.",
		"cygheap_user::init: NtSetInformationToken (TokenDefaultDacl), 0xC0000022",
	} {
		if WindowsRuntimeDiagnostic(text) == "" {
			t.Fatalf("missing diagnostic for %q", text)
		}
	}
}
