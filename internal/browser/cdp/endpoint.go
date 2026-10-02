package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const versionProbeLimit = 1 << 20

// resolveWebSocketURL turns a user-supplied endpoint into the browser-level
// DevTools socket URL. A ws:// or wss:// endpoint is used as given; anything
// else is probed at /json/version. A DevTools endpoint hands out full control
// of the browser and of every file it can read, so a non-loopback host is
// refused unless the caller opted in.
func resolveWebSocketURL(ctx context.Context, endpoint string, client *http.Client, allowRemote bool) (string, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return "", fmt.Errorf("cdp: endpoint is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("cdp: parse endpoint %q: %w", endpoint, err)
	}
	switch u.Scheme {
	case "ws", "wss":
		if err := checkHost(u, allowRemote); err != nil {
			return "", err
		}
		return u.String(), nil
	case "http", "https":
	default:
		return "", fmt.Errorf("cdp: endpoint %q must use http, https, ws, or wss", endpoint)
	}
	if err := checkHost(u, allowRemote); err != nil {
		return "", err
	}
	wsURL, err := probeVersion(ctx, u, client)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(wsURL)
	if err != nil {
		return "", fmt.Errorf("cdp: %s returned an unusable socket URL %q: %w", u.Host, wsURL, err)
	}
	if err := checkHost(parsed, allowRemote); err != nil {
		return "", err
	}
	return parsed.String(), nil
}

// probeVersion reads webSocketDebuggerUrl from /json/version.
func probeVersion(ctx context.Context, base *url.URL, client *http.Client) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	probe := *base
	probe.Path = strings.TrimSuffix(probe.Path, "/") + "/json/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.String(), nil)
	if err != nil {
		return "", fmt.Errorf("cdp: probe %s: %w", probe.String(), err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cdp: no DevTools endpoint at %s: %w", base.Host, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, versionProbeLimit))
	if err != nil {
		return "", fmt.Errorf("cdp: probe %s: %w", probe.String(), err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cdp: probe %s: status %d", probe.String(), resp.StatusCode)
	}
	var out struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("cdp: probe %s: %w", probe.String(), err)
	}
	if strings.TrimSpace(out.WebSocketDebuggerURL) == "" {
		return "", fmt.Errorf("cdp: %s reported no webSocketDebuggerUrl; start Chrome with --remote-debugging-port", base.Host)
	}
	return out.WebSocketDebuggerURL, nil
}

// checkHost keeps the control socket on the loopback interface unless the
// caller explicitly allowed a remote one.
func checkHost(u *url.URL, allowRemote bool) error {
	if allowRemote {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("cdp: endpoint host %q is not loopback; a DevTools endpoint grants full control of the browser and its files", host)
}

// waitForEndpoint polls until the DevTools socket URL resolves or ctx ends,
// which is how a freshly launched Chrome is met without a fixed sleep.
func waitForEndpoint(ctx context.Context, endpoint string, client *http.Client, allowRemote bool) (string, error) {
	var lastErr error
	for {
		wsURL, err := resolveWebSocketURL(ctx, endpoint, client, allowRemote)
		if err == nil {
			return wsURL, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("cdp: browser did not expose a DevTools endpoint: %w", lastErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
