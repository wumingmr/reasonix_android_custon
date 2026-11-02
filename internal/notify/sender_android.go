//go:build android

package notify

import (
	"os/exec"

	"reasonix/internal/secrets"
)

// PlatformSender delivers notifications through termux-api's
// termux-notification command (termux-api package + Termux:API app).
type PlatformSender struct{}

// NewPlatformSender returns the best-effort sender for the current platform.
func NewPlatformSender() PlatformSender { return PlatformSender{} }

func (PlatformSender) Send(m Message) error {
	cmd := exec.Command("termux-notification", "--title", m.Title, "--content", m.Body)
	cmd.Env = secrets.ProcessEnv()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
