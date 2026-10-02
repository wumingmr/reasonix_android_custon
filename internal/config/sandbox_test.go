package config

import "testing"

func TestBashModeResolvesToOffOnWindows(t *testing.T) {
	cfg := Default()
	if got := cfg.BashModeForGOOS("windows"); got != "off" {
		t.Fatalf("empty Windows bash mode = %q, want off", got)
	}

	// An explicit enforce stays readable but cannot request a backend the
	// platform does not have; doctor reports the ignored value.
	cfg.Sandbox.Bash = "enforce"
	if got := cfg.BashModeForGOOS("windows"); got != "off" {
		t.Fatalf("explicit Windows bash mode = %q, want off", got)
	}
	if got := cfg.BashModeForGOOS("linux"); got != "enforce" {
		t.Fatalf("explicit Linux bash mode = %q, want enforce", got)
	}

	cfg.Sandbox.Bash = ""
	if got := cfg.BashModeForGOOS("darwin"); got != "enforce" {
		t.Fatalf("empty Darwin bash mode = %q, want enforce", got)
	}
}
