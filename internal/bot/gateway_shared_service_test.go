package bot

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"reasonix/internal/session"
)

func TestEmbeddedBotUsesHostSessionServiceWithoutClosingIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	host, err := session.NewService("local", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	gw := &BotGateway{
		cfg: GatewayConfig{SessionServiceForRoot: func(got string) (*session.Service, error) {
			if got != root {
				t.Fatalf("store root = %q, want %q", got, root)
			}
			return host, nil
		}},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	service, err := gw.botSessionService(filepath.Join(filepath.Dir(root), "sessions"))
	if err != nil || service != host {
		t.Fatalf("bot service = %p, %v; want host %p", service, err, host)
	}
	gw.closeSessionServices()
	if _, err := host.Create(t.Context(), session.CreateOptions{SessionID: "still-open", CWD: t.TempDir(), Origin: session.SessionOriginNew}); err != nil {
		t.Fatalf("gateway closed host-owned service: %v", err)
	}
}
