package sandbox

import "testing"

func TestWindowsShellPolicy(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		for _, mode := range []string{"enforce", "off", ""} {
			for _, readOnly := range []bool{false, true} {
				for _, kind := range []ShellKind{ShellBash, ShellSh, ShellZsh, ShellPowerShell} {
					spec := Spec{Mode: mode, ReadOnly: readOnly}
					err := validateShellPolicy(goos, spec, Shell{Kind: kind, Path: "explicit-wrapper"})
					wantDenied := goos == "windows" && mode == "enforce" && kind != ShellPowerShell
					if (err != nil) != wantDenied {
						t.Fatalf("%s %s readOnly=%v %s: err=%v", goos, mode, readOnly, kind, err)
					}
				}
			}
		}
	}
}
