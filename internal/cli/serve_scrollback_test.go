package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/sandbox"
	"reasonix/internal/serve"
)

// The line serve prints goes into a tmux pane, and a sandboxed command (network
// on, the default) reads the pane and the named file back without the token.
func TestSandboxCannotReadServeTokenFromScrollback(t *testing.T) {
	if runtime.GOOS == "windows" || !sandbox.Available() {
		t.Skip("no sandbox")
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("no tmux")
	}
	t.Setenv("REASONIX_HOME", t.TempDir())
	for _, mode := range []string{"none", "token"} {
		ctrl := newOwnedTestController(t, control.Options{SessionDir: t.TempDir()})
		srv := serve.New(ctrl, serve.NewBroadcaster(), config.ServeConfig{AuthMode: mode})
		res := &serveFrontendResources{}
		opts := serveFrontendOptions{command: "serve"}
		if opts.launchTokenPath, err = launchTokenLocation(srv.AuthToken(), opts, res); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		reportServeAuth(&b, srv, "127.0.0.1:8787", opts)
		line := filepath.Join(t.TempDir(), "line.txt")
		_ = os.WriteFile(line, []byte(b.String()), 0o600)
		sock := "rv83jp" + mode
		if out, err := exec.Command(tmux, "-L", sock, "new-session", "-d", "-x", "400", "-s", "s", "cat "+line+"; sleep 20").CombinedOutput(); err != nil {
			t.Fatalf("tmux: %v %s", err, out)
		}
		time.Sleep(400 * time.Millisecond)
		work := t.TempDir()
		spec := sandbox.Spec{Mode: "enforce", WriteRoots: []string{work}, ForbidReadRoots: boot.RuntimeForbidReadRoots(config.Default(), work), Network: true}
		cmd := tmux + " -L " + sock + " capture-pane -p -J -t s"
		if opts.launchTokenPath != "" {
			cmd += "; echo ---; cat " + opts.launchTokenPath
		}
		argv, _ := sandbox.Command(spec, sandbox.Shell{Kind: sandbox.ShellBash, Path: "bash"}, cmd)
		out, _ := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		_ = exec.Command(tmux, "-L", sock, "kill-server").Run()
		res.release(false)
		ctrl.Close()
		if strings.Contains(string(out), srv.AuthToken()) {
			t.Errorf("mode=%s: sandboxed command recovered the launch token", mode)
		}
	}
}
