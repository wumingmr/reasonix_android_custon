package config

import (
	"fmt"
	"strings"
)

// BrowserConfig attaches a browser to sessions that have no Electron shell
// behind them (CLI, Serve, headless). It stays off by default because
// attaching means driving or launching a real Chrome; the desktop keeps its
// own built-in browser and ignores this section. Endpoint attaches to a
// running Chrome started with --remote-debugging-port, and an empty Endpoint
// launches one Reasonix owns and kills with the session. Either way the
// browser is not touched until a browser tool is actually called.
type BrowserConfig struct {
	Enabled  bool   `toml:"enabled"`
	Endpoint string `toml:"endpoint"`
	// AllowRemoteEndpoint permits a non-loopback endpoint. A DevTools endpoint
	// grants full control of the browser and of every file it can read.
	AllowRemoteEndpoint bool     `toml:"allow_remote_endpoint"`
	ChromePath          string   `toml:"chrome_path"`
	ChromeArgs          []string `toml:"chrome_args"`
	// UserDataDir is the launched browser's profile. Empty uses a throwaway
	// directory, so logins never outlive the session.
	UserDataDir string `toml:"user_data_dir"`
	Headless    bool   `toml:"headless"`
}

// renderBrowserConfig writes the [browser] table. Only values that differ from
// the defaults reach here, so the section stays absent for the common case of
// a session with no browser.
func renderBrowserConfig(b *strings.Builder, cfg BrowserConfig) {
	b.WriteString("[browser]\n")
	fmt.Fprintf(b, "enabled = %v   # browser tools for CLI/Serve sessions; the browser attaches on first use\n", cfg.Enabled)
	if cfg.Endpoint != "" {
		fmt.Fprintf(b, "endpoint = %q   # a running Chrome's --remote-debugging-port endpoint; empty launches one\n", cfg.Endpoint)
	}
	if cfg.AllowRemoteEndpoint {
		b.WriteString("allow_remote_endpoint = true   # a DevTools endpoint grants full control of that browser\n")
	}
	if cfg.ChromePath != "" {
		fmt.Fprintf(b, "chrome_path = %q\n", cfg.ChromePath)
	}
	if len(cfg.ChromeArgs) > 0 {
		fmt.Fprintf(b, "chrome_args = %s\n", renderStringArray(cfg.ChromeArgs))
	}
	if cfg.UserDataDir != "" {
		fmt.Fprintf(b, "user_data_dir = %q\n", cfg.UserDataDir)
	}
	if cfg.Headless {
		b.WriteString("headless = true\n")
	}
	b.WriteString("\n")
}
