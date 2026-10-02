// Command reasonix-cli-launcher is the stable Windows console entry point for
// versioned desktop installations. It delegates to the active full CLI and
// deliberately contains no Reasonix engine code.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"reasonix/internal/installlayout"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reasonix: locate CLI launcher:", err)
		os.Exit(1)
	}
	os.Exit(runCLI(exe, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runCLI(executable string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := filepath.Dir(filepath.Clean(executable))
	target, err := installlayout.ActiveCLIPathFor(root, "windows")
	if err != nil {
		fmt.Fprintln(stderr, "reasonix: resolve active CLI:", err)
		return 1
	}
	if same, err := installlayout.SameRegularFile(executable, target); err != nil {
		fmt.Fprintln(stderr, "reasonix: validate active CLI:", err)
		return 1
	} else if same {
		fmt.Fprintln(stderr, "reasonix: active CLI resolves to the launcher itself")
		return 1
	}
	cmd := exec.Command(target, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, "reasonix: start active CLI:", err)
		return 1
	}
	return 0
}
