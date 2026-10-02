//go:build windows

package builtin

import (
	"testing"

	"reasonix/internal/sandbox"
)

// Restricted presets must not demand an OS sandbox on Windows: the platform
// has none, so demanding one turned every shell call into a fail-closed
// "sandbox unavailable" error (#10292) instead of the tool-layer boundary the
// presets actually provide here.
func TestWindowsPresetsNeverDemandOSSandbox(t *testing.T) {
	previous := bashSandboxCommand
	bashSandboxCommand = func(spec sandbox.Spec, sh sandbox.Shell, command string) ([]string, bool) {
		if spec.Enforce() {
			t.Fatalf("Windows launch asked for confinement: %+v", spec)
		}
		return []string{sh.Path, "-Command", command}, false
	}
	t.Cleanup(func() { bashSandboxCommand = previous })
	sh := sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`}
	for _, preset := range []string{"read-only", "workspace-write", "danger-full-access"} {
		b := bash{shell: sh, workDir: t.TempDir(), sb: sandbox.Spec{Mode: "enforce", WriteRoots: []string{`C:\work`}}}
		ctx := sandbox.WithPermissionPreset(t.Context(), preset)
		if spec := b.specForCall(ctx); spec.Enforce() {
			t.Fatalf("%s: effective spec demands confinement: %+v", preset, spec)
		}
		prepared, lease, err := b.prepareLaunch(ctx, sh, "Write-Output ok", nil)
		if lease != nil {
			lease.Release()
		}
		if err != nil || prepared.Wrapped {
			t.Fatalf("%s: prepareLaunch wrapped=%v err=%v", preset, prepared.Wrapped, err)
		}
	}
}
