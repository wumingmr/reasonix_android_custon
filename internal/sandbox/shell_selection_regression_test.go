package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredShellPathCompatibilityAndCacheIdentity(t *testing.T) {
	exists := func(string) bool { return true }
	noWSL := func(string) bool { return false }
	for _, goos := range []string{"darwin", "linux"} {
		if got := configuredShellPathForPreference(goos, "powershell", "/portable/pwsh", exists, noWSL); got != "/portable/pwsh" {
			t.Fatalf("%s: legacy PowerShell preference lost its configured pwsh: %q", goos, got)
		}
		if shellInventoryKey(goos, "bash", "/portable/A/bash") == shellInventoryKey(goos, "bash", "/portable/a/bash") {
			t.Fatalf("%s: different configured executables share a capability snapshot", goos)
		}
	}
	if shellInventoryKey("windows", "PWSH", `C:\Portable\pwsh.exe`) != shellInventoryKey("windows", "pwsh", `c:\portable\PWSH.EXE`) {
		t.Fatal("Windows snapshot identity must remain case-insensitive")
	}
}

func TestConfiguredShellSnapshotUsesConfiguredExecutable(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			prefer, binary, id := "bash", "bash", ShellCapabilityBash
			if goos == "windows" {
				prefer, binary, id = "pwsh", "pwsh.exe", ShellCapabilityPwsh
			}
			configured := filepath.Join(t.TempDir(), binary)
			if err := os.WriteFile(configured, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			// A real snapshot must retain config provenance, not only manually
			// constructed unit-test snapshots. Non-Windows Bash probes are no-ops.
			snap := buildShellSnapshot(goos, prefer, configured)
			// On Windows, POSIX fixtures cannot execute a shebang; the native
			// configured PowerShell branch above needs no such probe.
			if goos != "windows" {
				snap.probeFunc = func(string) bool { return true }
				snap.probeCache = map[string]bool{}
				snap.caps = unixShellCapabilities(snap)
			}
			sh := resolveShell(prefer, configured, nil, goos, snap.lookPath, snap.exists, snap.bashCands, snap.psCands, snap.probe, snap.isWSL)
			if sh.Path != configured {
				t.Fatalf("runtime did not select config: %+v", sh)
			}
			for _, cap := range snap.caps {
				if cap.ID == id && (!cap.Available || cap.Path != configured || cap.Source != ShellSourceConfig) {
					t.Fatalf("config was lost during discovery: %+v", cap)
				}
			}
		})
	}
}

func TestWSLBashAliasesExcluded(t *testing.T) {
	const gitBash = `D:\PortableGit\bin\bash.exe`
	isWSL := func(p string) bool { return isWindowsWSLBashPath(p, `C:\Windows`) }
	for _, alias := range []string{
		`C:\Windows\System32\bash.exe`,
		`C:\Users\tester\AppData\Local\Microsoft\WindowsApps\bash.exe`,
		`C:/Users/tester/AppData/Local/MICROSOFT/WindowsApps/BASH.EXE`,
		`\\?\C:\Users\tester\AppData\Local\Microsoft\WindowsApps\bash.exe`,
		`C:\Users\tester\AppData\Local\Microsoft\WindowsApps\MicrosoftCorporationII.WindowsSubsystemForLinux_8wekyb3d8bbwe\bash.exe`,
	} {
		t.Run(alias, func(t *testing.T) {
			if !isWSL(alias) {
				t.Fatal("WSL launcher was accepted as native Bash")
			}
			lookup := fakeLookPath(map[string]string{"bash": alias})
			for _, configured := range []string{"", alias} {
				sh := resolveShell("bash", configured, nil, "windows", lookup, func(string) bool { return true }, []string{gitBash}, nil, func(string) bool { return true }, isWSL)
				if sh.Path != gitBash {
					t.Fatalf("configured=%q resolved=%+v; must skip even a healthy WSL alias", configured, sh)
				}
			}
			snap := &shellSnapshot{lookPath: lookup, exists: func(string) bool { return true }, isWSL: isWSL,
				bashCands: []string{gitBash}, probeFunc: func(string) bool { return true }, probeCache: map[string]bool{}}
			if cap := windowsShellCapabilities(snap)[0]; !cap.Available || cap.Path != gitBash {
				t.Fatalf("capability accepted WSL: %+v", cap)
			}
		})
	}
	if !isWindowsWSLBashPath(`C:\Users\tester\AppData\Local\Microsoft\WindowsApps\bash.exe`, "") {
		t.Fatal("alias exclusion must not depend on SystemRoot")
	}
	for _, native := range []string{"", gitBash, `C:\WindowsPortable\Git\bin\bash.exe`, `C:\Users\tester\Microsoft\WindowsAppsOther\bash.exe`} {
		if isWSL(native) {
			t.Fatalf("native path incorrectly excluded: %q", native)
		}
	}
}

func TestPowerShellPreferenceAndInventoryUseCompatibleConfiguredPath(t *testing.T) {
	const ps = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	const pwsh = `C:\Program Files\PowerShell\7\pwsh.exe`
	const portablePS = `D:\PortablePowerShell\powershell.exe`
	const portablePwsh = `D:\PortablePowerShell\pwsh.exe`
	const wrapper = `D:\Tools\ps-wrapper.exe`
	for _, tc := range []struct {
		name, prefer, configured, want string
		installed                      bool
	}{
		{"switch to Windows PowerShell", "powershell", portablePwsh, ps, true},
		{"switch to PowerShell 7", "pwsh", portablePS, pwsh, true},
		{"portable PS5", "powershell", portablePS, portablePS, false},
		{"portable PS7", "pwsh", portablePwsh, portablePwsh, false},
		{"config before standard", "pwsh", portablePwsh, portablePwsh, true},
		{"auto portable PS7", "auto", portablePwsh, portablePwsh, false},
		{"auto portable PS5", "auto", portablePS, portablePS, false},
		{"case insensitive", " PWSH ", portablePwsh, portablePwsh, false},
		{"intentional wrapper", "powershell", wrapper, wrapper, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := map[string]string{}
			var candidates []string
			if tc.installed {
				paths["powershell"], paths["pwsh"] = ps, pwsh
				candidates = []string{pwsh, ps}
			}
			lookup := fakeLookPath(paths)
			exists := func(p string) bool { return p == tc.configured || tc.installed && (p == ps || p == pwsh) }
			noWSL := func(string) bool { return false }
			sh := resolveShell(tc.prefer, tc.configured, nil, "windows", lookup, exists, nil, candidates, func(string) bool { return true }, noWSL)
			if sh.Path != tc.want {
				t.Fatalf("resolved=%+v, want %q", sh, tc.want)
			}
			snap := &shellSnapshot{prefer: tc.prefer, configPath: tc.configured, lookPath: lookup, exists: exists, isWSL: noWSL, psCands: candidates, probeCache: map[string]bool{}}
			id := ShellCapabilityPowerShell
			if sh.SupportsChaining() {
				id = ShellCapabilityPwsh
			}
			for _, cap := range windowsShellCapabilities(snap) {
				if cap.ID != id {
					continue
				}
				if !cap.Available || cap.Path != tc.want {
					t.Fatalf("runtime=%+v, capability=%+v", sh, cap)
				}
				if tc.want == tc.configured && cap.Source != ShellSourceConfig {
					t.Fatalf("custom path source=%q", cap.Source)
				}
			}
			if strings.TrimSpace(tc.prefer) != "auto" && tc.want != tc.configured && configuredShellPathForPreference("windows", tc.prefer, tc.configured, exists, noWSL) != "" {
				t.Fatal("shared path consumer retained the wrong PowerShell version")
			}
		})
	}
}
