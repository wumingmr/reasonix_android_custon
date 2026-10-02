package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"reasonix/internal/secrets"
)

func TestWindowsPowerShellCandidatesIncludeStoreAlias(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\PF`)
	t.Setenv("ProgramW6432", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LOCALAPPDATA", `C:\LAD`)
	t.Setenv("SystemRoot", `C:\WIN`)
	got := windowsPowerShellCandidates()
	want := []string{
		filepath.Join(`C:\PF`, "PowerShell", "7", "pwsh.exe"),
		filepath.Join(`C:\LAD`, "Microsoft", "WindowsApps", "pwsh.exe"),
		filepath.Join(`C:\WIN`, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
}

// A Store pwsh that is found but cannot start must not win auto-detection: the
// next interpreter that does start is selected instead.
func TestAutoSkipsPowerShellThatDoesNotLaunch(t *testing.T) {
	alias := `C:\fake\WindowsApps\pwsh.exe`
	winPS := []string{alias, `C:\fake\System32\powershell.exe`}
	launches := func(p string) bool { return p != alias && p != `C:\fake\pwsh.exe` }
	yes := func(string) bool { return true }
	no := func(string) bool { return false }

	lookPath := launchablePowerShellLookPath(fakeLookPath(map[string]string{"pwsh": `C:\fake\pwsh.exe`, "powershell": `C:\fake\powershell.exe`}), launches)
	got := resolveShell("", "", nil, "windows", lookPath, yes, nil, launchablePowerShells(winPS, launches), no, no)
	if got.Kind != ShellPowerShell || got.Path != `C:\fake\System32\powershell.exe` {
		t.Fatalf("auto = %+v, want Windows PowerShell 5.1", got)
	}

	launches = func(string) bool { return true }
	lookPath = launchablePowerShellLookPath(fakeLookPath(map[string]string{"pwsh": `C:\fake\pwsh.exe`, "powershell": `C:\fake\powershell.exe`}), launches)
	got = resolveShell("", "", nil, "windows", lookPath, yes, nil, launchablePowerShells(winPS, launches), no, no)
	if got.Path != alias {
		t.Fatalf("auto = %+v, want the Store pwsh when it launches", got)
	}
}

func TestLaunchablePowerShellLookPathLeavesOtherNamesAlone(t *testing.T) {
	lookPath := launchablePowerShellLookPath(fakeLookPath(map[string]string{"bash": `C:\fake\bash.exe`, "pwsh": `C:\fake\pwsh.exe`}), func(string) bool { return false })
	if p, err := lookPath("bash"); err != nil || p != `C:\fake\bash.exe` {
		t.Fatalf("bash = %q, %v", p, err)
	}
	if _, err := lookPath("pwsh"); err == nil {
		t.Fatal("pwsh that cannot launch was still found")
	}
}

// Only an execution alias pays a launch; a regular executable is taken as found.
func TestPowerShellLaunchesProbesOnlyAliases(t *testing.T) {
	probed := 0
	saved := aliasLaunches
	aliasLaunches = &launchProbes{seen: map[string]launchProbe{}, run: func(string) bool { probed++; return false }}
	t.Cleanup(func() { aliasLaunches = saved })

	regular := filepath.Join(t.TempDir(), "pwsh.exe")
	if err := os.WriteFile(regular, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !powerShellLaunches(regular) || probed != 0 {
		t.Fatalf("regular file: launches=%v probes=%d, want true without a probe", powerShellLaunches(regular), probed)
	}
}

func TestLaunchProbesAnswerRepeatedDiscoveryOnce(t *testing.T) {
	runs := 0
	l := &launchProbes{seen: map[string]launchProbe{}, run: func(string) bool { runs++; return true }}
	for range 3 {
		if !l.check(`C:\Users\u\AppData\Local\Microsoft\WindowsApps\pwsh.exe`) {
			t.Fatal("probe answer lost")
		}
	}
	if runs != 1 {
		t.Fatalf("probe ran %d times, want 1", runs)
	}
}

func TestPowerShellProbeRunsNothingOfTheUsers(t *testing.T) {
	cmd := powerShellProbeCommand(context.Background(), `C:\x\pwsh.exe`)
	if want := []string{`C:\x\pwsh.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "exit 0"}; !slices.Equal(cmd.Args, want) {
		t.Fatalf("args = %q", cmd.Args)
	}
	if !slices.Equal(cmd.Env, secrets.ProcessEnv()) || cmd.Dir != os.TempDir() {
		t.Fatalf("env/dir not scrubbed: dir=%q", cmd.Dir)
	}
}
