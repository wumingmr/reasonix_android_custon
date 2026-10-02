package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/serve"
	"reasonix/internal/stats"
)

func TestAuthDisabledServeKeepsLaunchTokenOffTheTerminal(t *testing.T) {
	// serve.New starts the process-wide usage catalog under REASONIX_HOME; it
	// must be closed before the home is removed, or Windows refuses the delete.
	closeUsage := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := stats.CloseUsageCatalogs(ctx); err != nil {
			t.Fatalf("close usage catalog: %v", err)
		}
	}
	closeUsage()
	t.Setenv("REASONIX_HOME", t.TempDir())
	t.Cleanup(closeUsage)
	ctrl := newOwnedTestController(t, control.Options{SessionDir: t.TempDir()})
	t.Cleanup(ctrl.Close)
	srv := serve.New(ctrl, serve.NewBroadcaster(), config.ServeConfig{AuthMode: "none"})
	resources := &serveFrontendResources{}
	opts := serveFrontendOptions{command: "serve"}
	path, err := launchTokenLocation(srv.AuthToken(), opts, resources)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.release(false)
	if filepath.Dir(path) != config.RemoteStateDir() {
		t.Fatalf("launch token file %s is outside the sandbox-denied %s", path, config.RemoteStateDir())
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(data)) != srv.AuthToken() {
		t.Fatalf("launch token file = %q, %v", data, err)
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("launch token file mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	opts.launchTokenPath = path
	var report strings.Builder
	reportServeAuth(&report, srv, "127.0.0.1:8787", opts)
	out := report.String()
	if strings.Contains(out, srv.AuthToken()) || !strings.Contains(out, path) {
		t.Fatalf("serve report printed the token or omitted its file:\n%s", out)
	}
	resources.release(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("launch token file outlived the serve: %v", err)
	}
}

func TestManagedAuthDisabledServePointsAtItsTokenFile(t *testing.T) {
	resources := &serveFrontendResources{}
	path, err := launchTokenLocation("secret", serveFrontendOptions{tokenFile: "/run/serve.token"}, resources)
	if err != nil || path != "/run/serve.token" || len(resources.artifacts) != 0 {
		t.Fatalf("managed launch token location = %q %v artifacts=%v", path, err, resources.artifacts)
	}
}
