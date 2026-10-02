//go:build windows

package desktoplauncher

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"reasonix/internal/config"
	"reasonix/internal/desktopinstance"
	"reasonix/internal/installlayout"
	"reasonix/internal/proc"
)

func coordinatedLaunch(root string, args []string) (bool, int) {
	interactive := os.Getenv("REASONIX_NONINTERACTIVE") != "1"
	return coordinatedLaunchWith(root, args, interactive, coordinatedLaunchDependencies{
		classifyLocation: classifyLaunchLocation,
		launchAndVerify:  desktopinstance.LaunchAndVerify,
		notify:           desktopinstance.Notify,
		logRejected:      desktopinstance.LogPortableLocationRejected,
		stderr:           os.Stderr,
	})
}

type coordinatedLaunchDependencies struct {
	classifyLocation func(string) (launchLocation, error)
	launchAndVerify  func(string, string, bool, func() error, ...string) error
	notify           func(error)
	logRejected      func(string, string, bool)
	stderr           io.Writer
}

func coordinatedLaunchWith(root string, args []string, interactive bool, deps coordinatedLaunchDependencies) (bool, int) {
	// Shell-less versioned layouts (pre-shell releases, partial rollbacks)
	// carry no shell lifecycle to coordinate or verify; launch directly.
	if !installlayout.HasCurrent(root) || !installlayout.HasActiveShell(root) {
		return false, 0
	}
	home := config.ReasonixHomeDir()
	if isPortableRoot(root) {
		location, err := deps.classifyLocation(root)
		if err != nil {
			return coordinatedLaunchError(err, interactive, deps)
		}
		if location == launchLocationUNC || location == launchLocationRemoteDrive {
			err := desktopinstance.NewUnsupportedPortableLocationError(string(location))
			deps.logRejected(home, string(location), interactive)
			return coordinatedLaunchError(err, interactive, deps)
		}
	}
	err := deps.launchAndVerify(root, home, interactive, func() error {
		path, err := ResolveDesktopPath(root)
		if err != nil {
			return err
		}
		cmd := exec.Command(path, StripLegacyLaunchArgs(args)...)
		proc.HideConsole(cmd)
		cmd.Dir = root
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}, StripLegacyLaunchArgs(args)...)
	if err != nil {
		return coordinatedLaunchError(err, interactive, deps)
	}
	return true, 0
}

func coordinatedLaunchError(err error, interactive bool, deps coordinatedLaunchDependencies) (bool, int) {
	fmt.Fprintln(deps.stderr, err)
	if interactive {
		deps.notify(err)
	}
	return true, desktopinstance.ExitCode(err)
}

func isPortableRoot(root string) bool {
	info, err := os.Lstat(filepath.Join(root, installlayout.PortableAliasName()))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	_, err = os.Lstat(filepath.Join(root, "uninstall.exe"))
	return os.IsNotExist(err)
}
