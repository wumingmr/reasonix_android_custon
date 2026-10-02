// Package sandbox wraps a shell command in an OS-level jail so the model's
// `bash` calls are confined: reads stay mostly free, writes stay inside the
// writable roots (workspace, extras, temp and toolchain caches), forbid-read
// roots are hidden, and network egress is optional. It is the *enforcement*
// layer beneath the permission rules: a permitted command still cannot escape.
// macOS uses Seatbelt and Linux uses bubblewrap; where a backend is missing,
// restricted presets fail closed. Windows has no OS-level shell sandbox (see
// OSSandboxSupported). File-writer built-ins are confined in tool/builtin.
package sandbox

import "runtime"

// Spec describes how to confine one command. The zero value (Mode == "") does
// not enforce, so an unconfigured caller runs commands unchanged.
type Spec struct {
	// Mode is "enforce" to wrap the command, anything else (incl. "off" and "")
	// to run it unwrapped.
	Mode string
	// ReadOnly removes every ordinary writable mount/allowance. It is distinct
	// from an empty WriteRoots slice, whose historical meaning is unconfigured.
	ReadOnly bool
	// WriteRoots are directories the command may write to (the workspace root
	// plus any configured extras). Platforms may add command-scoped temp/cache
	// roots so builds and package managers keep working without broad writes.
	WriteRoots []string
	// ForbidReadRoots are files or directories the command may not read from
	// when confined. The OS sandbox denies access to these paths (macOS Seatbelt
	// deny file-read* rules, Linux bubblewrap masks); on other platforms the
	// in-process tools enforce this instead.
	ForbidReadRoots []string
	// Network allows network egress from inside the sandbox. Off blocks it so a
	// command cannot exfiltrate or fetch; many dev commands (module/package
	// downloads) need it, so it defaults on at the config layer.
	Network bool
	// MinimalWrites omits the broad build-tool cache write allowances used by
	// the bash sandbox. MCP profiles set it and explicitly provide only their
	// private state/temp directories (plus approved writer roots).
	MinimalWrites bool
	// Shell is the interpreter the bash tool runs under. A zero value (empty
	// Path) means the tool resolves one itself; the composition root sets it from
	// [tools.shell] so the configured choice rides along with the spec.
	Shell Shell
	// SessionTemp is the absolute path of the logical-session private temporary
	// directory for this command. When set, Linux bubblewrap binds it at /tmp
	// (instead of a fresh tmpfs), and all platforms export TMPDIR/TMP/TEMP so
	// consecutive Bash calls in the same session share temporary files. Empty
	// keeps the platform default (ephemeral tmpfs on Linux bwrap, host temp
	// elsewhere). MCP and other independent sandboxes leave this empty.
	SessionTemp string
	// ProtectedWriteRoots are Reasonix session/state paths that stay read-only
	// even when a broader WriteRoot such as the user's home directory would
	// otherwise cover them.
	ProtectedWriteRoots []string
}

// Enforce reports whether the spec asks for confinement.
func (s Spec) Enforce() bool { return s.Mode == "enforce" }

// OSSandboxSupported reports whether this platform can confine shell commands
// at the OS level. Windows cannot: its restricted-token backend is retired
// (same-user ACL denies locked hosts out; the token broke common toolchains).
func OSSandboxSupported() bool { return osSandboxSupportedForGOOS(runtime.GOOS) }

func osSandboxSupportedForGOOS(goos string) bool { return goos != "windows" }

// UnavailableMessage explains why an enforced shell sandbox cannot run and gives
// the platform-specific remediation.
func UnavailableMessage() string {
	return "shell sandbox requested but unavailable on this host; refusing to run unconfined. " + UnavailableRemediation()
}

// UnavailableRemediation is split out so status surfaces can append the same
// actionable hint without repeating the leading error.
func UnavailableRemediation() string {
	switch runtime.GOOS {
	case "linux":
		return "Install bubblewrap (`bwrap`), or explicitly select Full access for an unconfined session."
	case "darwin":
		return "Ensure `sandbox-exec` is installed and usable (the host must allow `sandbox_apply`), or explicitly select Full access for an unconfined session."
	case "windows":
		return "Windows has no OS-level shell sandbox. Permission presets are enforced by Reasonix file tools and shell commands run as the current OS user; select Full access only when ordinary approval prompts should also be skipped."
	default:
		return "Restricted permission presets are unavailable on this platform; explicitly select Full access only when unconfined execution is intended."
	}
}

// BackendUnavailableReason is safe diagnostic copy for subsystems such as MCP
// that intentionally continue unconfined when the OS backend is missing.
func BackendUnavailableReason() string {
	switch runtime.GOOS {
	case "linux":
		return "bubblewrap (bwrap) is unavailable on PATH"
	case "darwin":
		return "sandbox-exec is missing from PATH or unusable (sandbox_apply is restricted)"
	case "windows":
		return "Reasonix does not ship an OS-level sandbox on Windows"
	default:
		return "this platform has no supported Reasonix sandbox backend"
	}
}
