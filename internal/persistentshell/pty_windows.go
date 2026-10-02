//go:build windows

package persistentshell

// A machine-driven Git Bash session must not pass through ConPTY: its screen
// rendering can rewrite non-ASCII bytes and completion markers. A long-lived
// pipe retains cwd/environment without terminal encoding, wrapping, or prompts.
func startPTY(argv []string, dir string, env []string) (ptyConn, error) {
	return startPipe(argv, dir, env)
}
