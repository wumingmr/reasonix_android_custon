//go:build windows

package persistentshell

import (
	"net"
	"os/exec"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"reasonix/internal/proc"
)

func listenShellControl(name string) (net.Listener, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(`\\.\pipe\`+name, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")", InputBufferSize: 65536, OutputBufferSize: 65536,
	})
}

func startShellTracked(cmd *exec.Cmd) (uintptr, error) { return proc.StartTrackedRequired(cmd) }
