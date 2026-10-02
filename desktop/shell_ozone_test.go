//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapShellChoosesOzonePlatformFromSession(t *testing.T) {
	x11 := "--ozone-platform=x11"
	cases := []struct {
		name string
		env  []string
		args []string
		want []string
	}{
		{"wayland with xwayland defaults to x11", []string{"XDG_SESSION_TYPE=wayland", "WAYLAND_DISPLAY=wayland-0", "DISPLAY=:0"}, []string{"--flag"}, []string{x11, "--flag"}},
		{"wayland display alone counts as wayland", []string{"WAYLAND_DISPLAY=wayland-0", "DISPLAY=:0"}, nil, []string{x11}},
		{"pure wayland stays native", []string{"XDG_SESSION_TYPE=wayland", "WAYLAND_DISPLAY=wayland-0"}, []string{"--flag"}, []string{"--flag"}},
		{"override wayland stays native", []string{"XDG_SESSION_TYPE=wayland", "DISPLAY=:0", "REASONIX_OZONE_PLATFORM=wayland"}, nil, nil},
		{"override x11 forces x11", []string{"XDG_SESSION_TYPE=x11", "DISPLAY=:0", "REASONIX_OZONE_PLATFORM=x11"}, nil, []string{x11}},
		{"override auto keeps the default", []string{"XDG_SESSION_TYPE=wayland", "DISPLAY=:0", "REASONIX_OZONE_PLATFORM=auto"}, nil, []string{x11}},
		{"explicit flag wins", []string{"XDG_SESSION_TYPE=wayland", "DISPLAY=:0"}, []string{"--ozone-platform=wayland"}, []string{"--ozone-platform=wayland"}},
		{"explicit hint wins", []string{"XDG_SESSION_TYPE=wayland", "DISPLAY=:0"}, []string{"--ozone-platform-hint=wayland"}, []string{"--ozone-platform-hint=wayland"}},
		{"x11 session unchanged", []string{"XDG_SESSION_TYPE=x11", "DISPLAY=:0"}, []string{"--flag"}, []string{"--flag"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, shell := writeShellBootstrapFixture(t, "linux")
			out := filepath.Join(t.TempDir(), "shell.out")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$REASONIX_TEST_SHELL_OUT.tmp\" && mv \"$REASONIX_TEST_SHELL_OUT.tmp\" \"$REASONIX_TEST_SHELL_OUT\"\n"
			if err := os.MkdirAll(filepath.Dir(shell), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			env := append([]string{"PATH=" + os.Getenv("PATH"), "REASONIX_TEST_SHELL_OUT=" + out}, tc.env...)
			if handled, code := bootstrapShell(exe, "linux", tc.args, env); !handled || code != 0 {
				t.Fatalf("bootstrap handled=%v code=%d", handled, code)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				raw, err := os.ReadFile(out)
				if err == nil {
					got := strings.Fields(string(raw))
					if strings.Join(got, " ") != strings.Join(tc.want, " ") {
						t.Fatalf("shell argv = %q, want %q", got, tc.want)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("shell never ran: %v", err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}
