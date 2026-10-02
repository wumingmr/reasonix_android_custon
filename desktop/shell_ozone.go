package main

import "strings"

const ozonePlatformEnv = "REASONIX_OZONE_PLATFORM"

// shellOzoneArgs prefixes --ozone-platform=x11 under a Wayland session that
// also offers XWayland: Electron picks Wayland by itself there, and on drivers
// whose EGL fails on that backend the shell dies before its first window
// (#10369). The flag must be in the shell's argv, because Chromium selects the
// Ozone backend before the main script runs.
func shellOzoneArgs(goos string, args []string, getenv func(string) string) []string {
	if goos != "linux" || hasOzoneSwitch(args) {
		return args
	}
	switch strings.ToLower(strings.TrimSpace(getenv(ozonePlatformEnv))) {
	case "wayland":
		return args
	case "x11":
		return append([]string{"--ozone-platform=x11"}, args...)
	}
	if !waylandSession(getenv) || getenv("DISPLAY") == "" {
		return args
	}
	return append([]string{"--ozone-platform=x11"}, args...)
}

func waylandSession(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("XDG_SESSION_TYPE"))) {
	case "wayland":
		return true
	case "x11":
		return false
	}
	return getenv("WAYLAND_DISPLAY") != ""
}

func hasOzoneSwitch(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		name, _, _ := strings.Cut(arg, "=")
		if name == "--ozone-platform" || name == "--ozone-platform-hint" {
			return true
		}
	}
	return false
}

func envLookup(env []string) func(string) string {
	return func(key string) string {
		value := ""
		for _, entry := range env {
			if k, v, ok := strings.Cut(entry, "="); ok && k == key {
				value = v
			}
		}
		return value
	}
}
