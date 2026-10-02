package sandbox

import (
	"fmt"
	"runtime"
)

// ValidateShellPolicy rejects the unsupported MSYS execution lane before any
// process or private-temp lease is created. Full-access explicit Bash remains
// supported; auto selection on Windows never selects Bash.
func ValidateShellPolicy(spec Spec, sh Shell) error {
	return validateShellPolicy(runtime.GOOS, spec, sh)
}

func validateShellPolicy(goos string, spec Spec, sh Shell) error {
	if goos == "windows" && spec.Enforce() && sh.Kind != ShellPowerShell {
		return fmt.Errorf("Bash/POSIX shell execution is disabled in the Windows restricted sandbox because MSYS/Cygwin runtime initialization is incompatible. Select auto, pwsh or powershell in Shell settings and use PowerShell syntax. The requested command was not run; sandbox permissions were not changed")
	}
	return nil
}
