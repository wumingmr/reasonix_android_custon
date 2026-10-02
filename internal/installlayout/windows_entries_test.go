package installlayout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/filelock"
)

func windowsEntryRequest(t *testing.T, root, version, body string) ActivationRequest {
	t.Helper()
	src := t.TempDir()
	req := ActivationRequest{InstallRoot: root, Version: version}
	for _, name := range AllowedVersionMembers() {
		req.Members = append(req.Members, Member{Name: name, Path: writeTempMember(t, src, name, body)})
	}
	req.WindowsRootEntries = &WindowsRootEntrySources{
		LauncherPath: writeTempMember(t, src, "payload-launcher", body+"-gui"),
		CLIEntryPath: writeTempMember(t, src, "payload-cli", body+"-cli"),
	}
	return req
}

func assertEntryBytes(t *testing.T, root, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q (%v), want %q", name, got, err, want)
	}
}

func TestWindowsEntriesFreshRepairAndUpgrade(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, canonical := range []bool{false, true} {
			t.Run(map[bool]string{false: "fresh", true: "legacy"}[legacy]+map[bool]string{false: "", true: "-canonical"}[canonical], func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "Reasonix 测试")
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
				if legacy {
					writeTempMember(t, root, "reasonix-launcher.exe", "old")
				}
				if canonical {
					writeTempMember(t, root, "Reasonix.exe", "old")
				}
				for i, version := range []string{"v1.38.9", "v1.38.9", "v1.39.0"} {
					body := []string{"install", "repair", "upgrade"}[i]
					if err := ActivateVersion(windowsEntryRequest(t, root, version, body)); err != nil {
						t.Fatal(err)
					}
					assertEntryBytes(t, root, "Reasonix.exe", body+"-gui")
					assertEntryBytes(t, root, "reasonix-cli.exe", body+"-cli")
					if legacy {
						assertEntryBytes(t, root, "reasonix-launcher.exe", body+"-gui")
					} else if _, err := os.Lstat(filepath.Join(root, "reasonix-launcher.exe")); !os.IsNotExist(err) {
						t.Fatalf("created legacy entry: %v", err)
					}
					ptr, err := ReadCurrent(root)
					if err != nil || ptr.ActiveVersion != version || ptr.SchemaVersion != 1 {
						t.Fatalf("pointer=%+v err=%v", ptr, err)
					}
				}
			})
		}
	}
}

func TestWindowsEntriesInspectLegacyAfterLockAcquisition(t *testing.T) {
	root := t.TempDir()
	req := windowsEntryRequest(t, root, "v1.39.0", "new")
	// Simulate the previous writer publishing the legacy entry while this
	// activation waits to acquire its lock. No sleeps or global test hooks.
	err := activateVersion(req, func(ctx context.Context, path string) (func(), error) {
		writeTempMember(t, root, "reasonix-launcher.exe", "previous-writer")
		return filelock.Acquire(ctx, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEntryBytes(t, root, "reasonix-launcher.exe", "new-gui")
}

func TestConcurrentOldAndNewActivationPreservesCommittedLegacyEntry(t *testing.T) {
	root := t.TempDir()
	first := windowsEntryRequest(t, root, "v1.38.9", "old")
	first.RootMembers = []Member{
		{Name: "Reasonix.exe", Path: first.WindowsRootEntries.LauncherPath},
		{Name: "reasonix-launcher.exe", Path: first.WindowsRootEntries.LauncherPath},
		{Name: "reasonix-cli.exe", Path: first.WindowsRootEntries.CLIEntryPath},
	}
	first.RequiredRootNames = []string{"Reasonix.exe", "reasonix-launcher.exe", "reasonix-cli.exe"}
	first.WindowsRootEntries = nil // Model the old writer's explicit entry list.
	second := windowsEntryRequest(t, root, "v1.39.0", "new")
	firstLocked, releaseFirst, secondAcquiring := make(chan struct{}), make(chan struct{}), make(chan struct{})
	checks := 0
	first.CheckProcesses = func() error {
		checks++
		if checks == 1 {
			close(firstLocked)
			<-releaseFirst
		}
		return nil
	}
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { firstDone <- ActivateVersion(first) }()
	select {
	case <-firstLocked:
	case err := <-firstDone:
		t.Fatalf("first activation never reached publication: %v", err)
	}
	go func() {
		secondDone <- activateVersion(second, func(ctx context.Context, path string) (func(), error) {
			close(secondAcquiring)
			return filelock.Acquire(ctx, path)
		})
	}()
	select {
	case <-secondAcquiring:
	case err := <-secondDone:
		close(releaseFirst)
		<-firstDone
		t.Fatalf("second activation never acquired its lock: %v", err)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	assertEntryBytes(t, root, "Reasonix.exe", "new-gui")
	assertEntryBytes(t, root, "reasonix-launcher.exe", "new-gui")
	assertEntryBytes(t, root, "reasonix-cli.exe", "new-cli")
	ptr, err := ReadCurrent(root)
	if err != nil || ptr.ActiveVersion != "v1.39.0" {
		t.Fatalf("pointer=%+v err=%v", ptr, err)
	}
}

func TestWindowsEntriesRollbackAllSelectedEntries(t *testing.T) {
	for _, failure := range []string{"process", "pointer", "root"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			writeTempMember(t, root, "reasonix-launcher.exe", "original")
			if err := ActivateVersion(windowsEntryRequest(t, root, "v1.38.9", "old")); err != nil {
				t.Fatal(err)
			}
			req := windowsEntryRequest(t, root, "v1.39.0", "new")
			checks := 0
			switch failure {
			case "process":
				req.CheckProcesses = func() error {
					checks++
					if checks == 2 {
						return errors.New("process appeared")
					}
					return nil
				}
			case "pointer":
				if err := os.Remove(filepath.Join(root, CurrentFileName)); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, CurrentFileName), 0o755); err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := os.Remove(filepath.Join(root, "reasonix-cli.exe")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "reasonix-cli.exe"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := ActivateVersion(req); err == nil {
				t.Fatal("activation should fail")
			}
			assertEntryBytes(t, root, "Reasonix.exe", "old-gui")
			assertEntryBytes(t, root, "reasonix-launcher.exe", "old-gui")
			if failure != "root" {
				assertEntryBytes(t, root, "reasonix-cli.exe", "old-cli")
			}
			if failure != "pointer" {
				ptr, err := ReadCurrent(root)
				if err != nil || ptr.ActiveVersion != "v1.38.9" {
					t.Fatalf("pointer=%+v err=%v", ptr, err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "versions", "v1.39.0")); !os.IsNotExist(err) {
				t.Fatalf("uncommitted version survived: %v", err)
			}
		})
	}
}

func TestWindowsEntriesRejectInvalidLegacyAndMixedInputs(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "mixed"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			req := windowsEntryRequest(t, root, "v1.39.0", "new")
			legacy := filepath.Join(root, "reasonix-launcher.exe")
			switch kind {
			case "directory":
				if err := os.Mkdir(legacy, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(req.WindowsRootEntries.LauncherPath, legacy); err != nil {
					t.Skipf("symlink privilege unavailable: %v", err)
				}
			case "mixed":
				req.RootMembers = []Member{{Name: "other", Path: req.WindowsRootEntries.LauncherPath}}
			}
			if err := ActivateVersion(req); err == nil {
				t.Fatal("invalid request accepted")
			}
			if HasCurrent(root) {
				t.Fatal("invalid activation committed")
			}
			if _, err := os.Lstat(filepath.Join(root, "Reasonix.exe")); !os.IsNotExist(err) {
				t.Fatalf("published root entry: %v", err)
			}
		})
	}
}
