package cdp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"reasonix/internal/proc"
)

// activePortFile is where Chrome records the port it actually bound, which is
// the only reliable answer when the launcher asks for port 0.
const activePortFile = "DevToolsActivePort"

// launched is one Chrome this package started and therefore must reap.
type launched struct {
	cmd    *exec.Cmd
	handle uintptr
	dir    string
	owned  bool

	once sync.Once
}

// baseArgs keeps a launched Chrome out of the user's profile and away from the
// background services that make a first run slow and chatty.
func baseArgs(userDataDir string, headless bool) []string {
	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-features=Translate,MediaRouter",
		"--password-store=basic",
		"--window-size=1280,900",
	}
	if runtime.GOOS == "darwin" {
		args = append(args, "--use-mock-keychain")
	}
	if headless {
		args = append(args, "--headless=new", "--hide-scrollbars")
	}
	return args
}

// launchChrome starts a browser and returns it with its DevTools socket URL.
func launchChrome(ctx context.Context, opts Options) (*launched, string, error) {
	bin, err := chromePath(opts.ChromePath)
	if err != nil {
		return nil, "", err
	}
	dir, owned, err := userDataDir(opts.UserDataDir)
	if err != nil {
		return nil, "", err
	}
	args := append(baseArgs(dir, opts.Headless), opts.ChromeArgs...)
	cmd := proc.CommandContext(ctx, bin, append(args, "about:blank")...)
	var errBuf bytes.Buffer
	cmd.Stderr = &boundedWriter{buf: &errBuf, limit: 8 << 10}
	handle, err := proc.StartTracked(cmd)
	if err != nil {
		cleanupDir(dir, owned)
		return nil, "", fmt.Errorf("cdp: start %s: %w", bin, err)
	}
	l := &launched{cmd: cmd, handle: handle, dir: dir, owned: owned}
	wsURL, err := readActivePort(ctx, dir, opts.LaunchTimeout)
	if err != nil {
		l.stop()
		if detail := strings.TrimSpace(errBuf.String()); detail != "" {
			return nil, "", fmt.Errorf("%w: %s", err, lastLine(detail))
		}
		return nil, "", err
	}
	return l, wsURL, nil
}

// stop kills the browser tree and drops a user-data directory this package
// created. A directory the caller supplied is left alone: it is their profile.
func (l *launched) stop() {
	l.once.Do(func() {
		proc.KillTracked(l.cmd, l.handle)
		_ = l.cmd.Wait()
		proc.FinishTracked(l.handle)
		cleanupDir(l.dir, l.owned)
	})
}

func cleanupDir(dir string, owned bool) {
	if owned && dir != "" {
		_ = os.RemoveAll(dir)
	}
}

func userDataDir(configured string) (string, bool, error) {
	if dir := strings.TrimSpace(configured); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", false, fmt.Errorf("cdp: user data dir %s: %w", dir, err)
		}
		return dir, false, nil
	}
	dir, err := os.MkdirTemp("", "reasonix-chrome-")
	if err != nil {
		return "", false, fmt.Errorf("cdp: temporary user data dir: %w", err)
	}
	return dir, true, nil
}

// readActivePort waits for Chrome to publish its bound port and socket path.
func readActivePort(ctx context.Context, dir string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	path := filepath.Join(dir, activePortFile)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 && strings.TrimSpace(lines[0]) != "" {
				return fmt.Sprintf("ws://127.0.0.1:%s%s", strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])), nil
			}
		}
		select {
		case <-deadline.Done():
			return "", fmt.Errorf("cdp: browser did not write %s within %s", activePortFile, timeout)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// chromePath resolves the browser binary: the configured path first, then the
// usual install locations for Chrome, Chromium, and Chromium-based Edge.
func chromePath(configured string) (string, error) {
	if p := strings.TrimSpace(configured); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("cdp: chrome_path %s: %w", p, err)
		}
		return p, nil
	}
	for _, env := range []string{"REASONIX_CHROME", "CHROME_PATH"} {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	for _, candidate := range chromeCandidates() {
		if candidate == "" {
			continue
		}
		if strings.ContainsRune(candidate, os.PathSeparator) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
			continue
		}
		if p, err := exec.LookPath(candidate); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("cdp: no Chrome, Chromium, or Edge binary found; set browser.chrome_path or REASONIX_CHROME")
}

func chromeCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			root := os.Getenv(env)
			if root == "" {
				continue
			}
			out = append(out,
				filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`))
		}
		return out
	default:
		return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"}
	}
}

// boundedWriter keeps only the first limit bytes of a child's stderr so a
// noisy browser cannot grow the parent's memory.
type boundedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.buf.Write(p[:room])
	}
	return len(p), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
