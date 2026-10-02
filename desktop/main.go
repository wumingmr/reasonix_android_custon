// Command reasonix-desktop is the Reasonix desktop service: the Go-side
// control.Controller and platform integrations, driven by the Electron shell
// over the desktop host protocol (--host-rpc on stdin/stdout). A plain launch
// bootstraps the Electron shell installed beside this binary (app/) and exits;
// the shell then restarts this binary as its --host-rpc service. It lives in a
// nested module (reasonix/desktop) so the desktop build never touches the
// CLI's CGO_ENABLED=0 single-static-binary guarantee, while still importing
// the same internal/* kernel.
package main

import (
	"fmt"
	"os"
	"strings"

	"reasonix/internal/skill/skillwatch"
	"reasonix/internal/winaclresidue"

	// Blank imports wire compile-time built-ins into their registries, exactly as
	// cmd/reasonix does — boot.Build resolves providers/tools from these registries.
	_ "reasonix/internal/provider/anthropic"
	_ "reasonix/internal/provider/openai"
	_ "reasonix/internal/provider/responses"
	_ "reasonix/internal/tool/builtin"
)

// version is injected at build time via `-ldflags "-X main.version=..."`,
// mirroring cmd/reasonix/main.go. The auto-updater reads it (App.Version) to compare
// against the published manifest; an un-injected dev build stays "dev" and never
// prompts to update.
var version = "dev"

// channel records the build's release line, injected via
// `-X main.channel=preview`. Default "stable" tracks the public release;
// "preview" tracks the opt-in test line. Legacy "canary" builds are treated as
// preview for compatibility.
var channel = "stable"

// macSelfUpdate is injected as "true" only for Developer ID signed + notarized
// macOS release builds. Local/ad-hoc macOS builds keep the manual download path.
var macSelfUpdate = "false"

func macSelfUpdateAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(macSelfUpdate)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func main() {
	// Contract generation is a build-time operation. Dispatch it before crash
	// capture so packaging cannot create files in the operator's Reasonix home.
	if dir, ok := emitContractDir(os.Args[1:]); ok {
		os.Exit(runEmitContract(dir))
	}
	// The shell asks for this before Chromium starts a child; it touches no
	// Reasonix state, so it runs ahead of crash capture and every other mode.
	if app, ok := stripPackageGrantsRequest(os.Args[1:]); ok {
		os.Exit(stripPackageGrants(os.Stdout, os.Stderr, app))
	}
	// Internal watcher-helper entry: the host-shared skill watch service
	// re-enters this executable so Windows directory watching never runs
	// in-process. Dispatch before any application initialization.
	if skillwatch.MaybeRunHelper() {
		return
	}
	// Older Windows builds could leave sandbox ACL residue behind after a
	// crash; sweep it in the background so startup never waits on icacls.
	go winaclresidue.SweepStaleMarkers()
	// The detached macOS self-update child must run before any shell starts.
	if handled, exitCode := maybeRunMacUpdateHandoff(os.Args[1:]); handled {
		os.Exit(exitCode)
	}
	capturePreviousFatalCrash()
	installFatalCrashOutput()
	exitIfHostLaunchMode(os.Args[1:])
	if maybeRelaunchIfSuperseded() {
		return
	}
	exitIfShellBootstrapped(os.Args[1:])

	fmt.Fprintln(os.Stderr, "reasonix-desktop: no Electron desktop shell (app/) is installed beside this binary; reinstall Reasonix or run the packaged desktop app")
	os.Exit(1)
}
