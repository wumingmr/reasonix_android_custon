//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"reasonix/internal/installlayout"
)

func writeShellBootstrapFixture(t *testing.T, goos string) (exe, shell string) {
	t.Helper()
	root := t.TempDir()
	name := installlayout.ShellExecutableNameFor(goos)
	if goos == "darwin" {
		dir := filepath.Join(root, "Reasonix.app", "Contents", "MacOS")
		exe, shell = filepath.Join(dir, "reasonix-desktop"), filepath.Join(dir, name)
	} else {
		exe, shell = filepath.Join(root, "reasonix-desktop"), filepath.Join(root, installlayout.AppShellDirName, name)
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("service"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe, shell
}

func TestBootstrapShellExecutableBesideResolvesPerPlatform(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "darwin"} {
		exe, shell := writeShellBootstrapFixture(t, goos)
		if got, ok := shellExecutableBeside(exe, goos); ok {
			t.Fatalf("%s: resolved %s without an installed shell", goos, got)
		}
		if err := os.MkdirAll(filepath.Dir(shell), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(shell, []byte("shell"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, ok := shellExecutableBeside(exe, goos)
		if !ok || got != shell {
			t.Fatalf("%s: shell = %q ok=%v, want %q", goos, got, ok, shell)
		}
	}
}

func TestMacBundleContentsRecognizesSupportedServiceLayouts(t *testing.T) {
	app := filepath.Join(t.TempDir(), "parent.app cache", "应用.app", "Contents")
	for _, relative := range []string{"MacOS/reasonix-desktop", "Resources/service/reasonix-desktop"} {
		exe := filepath.Join(app, filepath.FromSlash(relative))
		if got, ok := macAppContentsForExecutable(exe); !ok || got != app {
			t.Fatalf("%s: contents = %q ok=%v, want %q", relative, got, ok, app)
		}
	}
	for _, exe := range []string{
		filepath.Join(app, "Resources", "reasonix-desktop"),
		filepath.Join(t.TempDir(), "reasonix-desktop"),
		"relative/reasonix-desktop",
	} {
		if got, ok := macAppContentsForExecutable(exe); ok {
			t.Fatalf("unexpected contents %q for %q", got, exe)
		}
	}
}

func TestMacResourcesServiceFindsBundleShell(t *testing.T) {
	contents := filepath.Join(t.TempDir(), "Reasonix.app", "Contents")
	exe := filepath.Join(contents, "Resources", "service", "reasonix-desktop")
	want := filepath.Join(contents, "MacOS", "Reasonix")
	if got := shellPathForExecutable(exe, "darwin"); got != want {
		t.Fatalf("shell = %q, want %q", got, want)
	}
}

func TestBootstrapShellExecutableBesideNeverReturnsItself(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Reasonix.app", "Contents", "MacOS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, installlayout.ShellExecutableNameFor("darwin"))
	if err := os.WriteFile(exe, []byte("wails desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := shellExecutableBeside(exe, "darwin"); ok {
		t.Fatalf("resolved the running executable %s as the shell", got)
	}
}

func TestBootstrapShellStartsDetachedShellWithServiceEnvAndArgs(t *testing.T) {
	exe, shell := writeShellBootstrapFixture(t, runtime.GOOS)
	out := filepath.Join(t.TempDir(), "shell.out")
	script := "#!/bin/sh\nprintf '%s\\n' \"$REASONIX_DESKTOP_SERVICE\" \"$@\" > \"$REASONIX_TEST_SHELL_OUT\"\n"
	if err := os.MkdirAll(filepath.Dir(shell), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "REASONIX_TEST_SHELL_OUT="+out, shellServiceEnv+"=stale", ozonePlatformEnv+"=wayland")
	handled, code := bootstrapShell(exe, runtime.GOOS, []string{"--flag", "two words"}, env)
	if !handled || code != 0 {
		t.Fatalf("bootstrap handled=%v code=%d", handled, code)
	}
	deadline := time.Now().Add(10 * time.Second)
	want := []string{exe, "--flag", "two words"}
	var raw []byte
	for {
		var err error
		if raw, err = os.ReadFile(out); err == nil {
			got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			if strings.Join(got, "\x00") == strings.Join(want, "\x00") {
				break
			}
		}
		if time.Now().After(deadline) {
			got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			t.Fatalf("shell saw %q, want %q (read error: %v)", got, want, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBootstrapShellSkipsWithoutShellBesideBinary(t *testing.T) {
	if handled, _ := bootstrapShell(filepath.Join(t.TempDir(), "missing"), runtime.GOOS, nil, nil); handled {
		t.Fatal("bootstrap handled a binary without a shell beside it")
	}
}

func TestBootstrapShellIgnoresHostLaunchModes(t *testing.T) {
	for _, args := range [][]string{{"--host-rpc"}, {"-emit-contract", t.TempDir()}, {"--emit-contract=" + t.TempDir()}} {
		if handled, _ := maybeBootstrapShell(args); handled {
			t.Fatalf("args %v must never bootstrap the shell", args)
		}
	}
}

func TestLinuxBootstrapMatchesPackagedLayouts(t *testing.T) {
	for exe, want := range map[string]string{
		"/opt/reasonix/reasonix-desktop":                  "/opt/reasonix/app/Reasonix",
		"/opt/reasonix/versions/v1.39.0/reasonix-desktop": "/opt/reasonix/versions/v1.39.0/app/Reasonix",
		"/usr/bin/reasonix-desktop":                       "/usr/lib/reasonix/app/Reasonix",
	} {
		if got := shellPathForExecutable(exe, "linux"); got != want {
			t.Errorf("%s: got %s want %s", exe, got, want)
		}
	}
}
