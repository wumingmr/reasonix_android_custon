//go:build !windows

package persistentshell

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"reasonix/internal/proc"
)

// .NET maps named pipes to these Unix sockets. This also permits exercising
// the same control protocol with pwsh in POSIX development environments.
func listenShellControl(name string) (net.Listener, error) {
	return net.Listen("unix", filepath.Join(os.TempDir(), "CoreFxPipe_"+name))
}
func startShellTracked(cmd *exec.Cmd) (uintptr, error) {
	proc.SetProcessGroupKill(cmd)
	return proc.StartTracked(cmd)
}
