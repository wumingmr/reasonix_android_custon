//go:build windows

package desktoplauncher

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/desktopinstance"
	"reasonix/internal/installlayout"
)

func TestPortableRemoteLocationStopsBeforeDesktopCoordination(t *testing.T) {
	for _, test := range []struct {
		name        string
		interactive bool
		location    launchLocation
	}{
		{name: "UNC interactive", interactive: true, location: launchLocationUNC},
		{name: "UNC noninteractive", interactive: false, location: launchLocationUNC},
		{name: "mapped remote drive", interactive: true, location: launchLocationRemoteDrive},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := coordinatedLaunchFixture(t, true)
			var stderr bytes.Buffer
			launched, notified, logged := false, false, false
			handled, code := coordinatedLaunchWith(root, nil, test.interactive, coordinatedLaunchDependencies{
				classifyLocation: func(path string) (launchLocation, error) {
					if path != root {
						t.Fatalf("classified path = %q, want %q", path, root)
					}
					return test.location, nil
				},
				launchAndVerify: func(string, string, bool, func() error, ...string) error {
					launched = true
					return nil
				},
				notify: func(err error) {
					notified = true
					var failure *desktopinstance.Error
					if !errors.As(err, &failure) || failure.Code != desktopinstance.UnsupportedPortableLocation {
						t.Errorf("notification error = %v", err)
					}
				},
				logRejected: func(_ string, locationType string, gotInteractive bool) {
					logged = true
					if locationType != string(test.location) || gotInteractive != test.interactive {
						t.Errorf("logged location=%q interactive=%v", locationType, gotInteractive)
					}
				},
				stderr: &stderr,
			})
			if !handled || code != 1 {
				t.Fatalf("handled=%v code=%d, want true/1", handled, code)
			}
			if launched {
				t.Fatal("desktop coordination started for a rejected location")
			}
			if notified != test.interactive {
				t.Fatalf("notified=%v, want %v", notified, test.interactive)
			}
			if !logged {
				t.Fatal("rejection was not logged")
			}
			if !bytes.Contains(stderr.Bytes(), []byte(desktopinstance.UnsupportedPortableLocation)) {
				t.Fatalf("stderr = %q", stderr.String())
			}
			for _, want := range []string{"Copy the entire extracted folder", "请将整个解压目录复制到 Windows 本地磁盘", "use the installer", "或使用安装器"} {
				if !bytes.Contains(stderr.Bytes(), []byte(want)) {
					t.Errorf("stderr is missing %q: %q", want, stderr.String())
				}
			}
		})
	}
}

func TestPortableLocalAndInstalledRemoteLocationsKeepExistingLaunchPath(t *testing.T) {
	for _, test := range []struct {
		name           string
		portable       bool
		location       launchLocation
		wantClassified bool
	}{
		{name: "portable local", portable: true, location: launchLocationLocal, wantClassified: true},
		{name: "installed remote", portable: false, location: launchLocationUNC, wantClassified: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := coordinatedLaunchFixture(t, test.portable)
			classified, launched := false, false
			handled, code := coordinatedLaunchWith(root, []string{"--safe-mode", "workspace"}, false, coordinatedLaunchDependencies{
				classifyLocation: func(string) (launchLocation, error) {
					classified = true
					return test.location, nil
				},
				launchAndVerify: func(_ string, _ string, interactive bool, _ func() error, args ...string) error {
					launched = true
					if interactive {
						t.Error("noninteractive launch became interactive")
					}
					if len(args) != 1 || args[0] != "workspace" {
						t.Errorf("launch args = %q", args)
					}
					return nil
				},
				notify:      func(error) { t.Fatal("unexpected notification") },
				logRejected: func(string, string, bool) { t.Fatal("unexpected rejection log") },
				stderr:      &bytes.Buffer{},
			})
			if !handled || code != 0 || !launched {
				t.Fatalf("handled=%v code=%d launched=%v", handled, code, launched)
			}
			if classified != test.wantClassified {
				t.Fatalf("classified=%v, want %v", classified, test.wantClassified)
			}
		})
	}
}

func TestPortableLocationClassificationFailureIsGenericLaunchError(t *testing.T) {
	root := coordinatedLaunchFixture(t, true)
	queryErr := errors.New("drive type unavailable")
	var stderr bytes.Buffer
	notified, logged, launched := false, false, false
	handled, code := coordinatedLaunchWith(root, nil, true, coordinatedLaunchDependencies{
		classifyLocation: func(string) (launchLocation, error) { return "", queryErr },
		launchAndVerify: func(string, string, bool, func() error, ...string) error {
			launched = true
			return nil
		},
		notify:      func(err error) { notified = errors.Is(err, queryErr) },
		logRejected: func(string, string, bool) { logged = true },
		stderr:      &stderr,
	})
	if !handled || code != 1 || launched || !notified || logged {
		t.Fatalf("handled=%v code=%d launched=%v notified=%v logged=%v", handled, code, launched, notified, logged)
	}
	if !bytes.Contains(stderr.Bytes(), []byte(queryErr.Error())) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func coordinatedLaunchFixture(t *testing.T, portable bool) string {
	t.Helper()
	root := t.TempDir()
	version := "v1.38.9-2"
	active := filepath.Join(root, "versions", version)
	if err := os.MkdirAll(filepath.Join(active, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "reasonix-desktop.exe"), []byte("desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installlayout.WriteCurrent(root, installlayout.CurrentPointer{
		SchemaVersion: 1,
		ActiveVersion: version,
		ActiveDir:     filepath.ToSlash(filepath.Join("versions", version)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, installlayout.PortableAliasName()), []byte("launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	if portable {
		return root
	}
	if err := os.WriteFile(filepath.Join(root, "uninstall.exe"), []byte("uninstaller"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}
