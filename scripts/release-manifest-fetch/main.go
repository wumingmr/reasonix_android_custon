package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const manifestURL = "https://dl.reasonix.io/latest/latest.json"
const maxManifestBytes = 1 << 20

var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var stableManifestVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func updaterUserAgent(version string) string {
	return fmt.Sprintf("Reasonix-Updater/v%s (%s/%s; build=stable; update=stable)", version, runtime.GOOS, runtime.GOARCH)
}

func fetchManifest(client *http.Client, endpoint, version string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", updaterUserAgent(version))
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("cf-mitigated") != "" {
		return nil, fmt.Errorf("public Stable manifest access failed: HTTP %d, cf-mitigated=%q, cf-ray=%q",
			response.StatusCode, response.Header.Get("cf-mitigated"), response.Header.Get("cf-ray"))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxManifestBytes {
		return nil, fmt.Errorf("public Stable manifest exceeds %d bytes", maxManifestBytes)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("public Stable manifest is not JSON: %w", err)
	}
	if !stableManifestVersion.MatchString(manifest.Version) {
		return nil, fmt.Errorf("public Stable manifest has no valid Stable version")
	}
	return body, nil
}

func main() {
	if len(os.Args) != 3 || !stableVersion.MatchString(os.Args[1]) || strings.TrimSpace(os.Args[2]) == "" {
		fmt.Fprintln(os.Stderr, "usage: release-manifest-fetch VERSION OUTPUT")
		os.Exit(2)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	body, err := fetchManifest(client, manifestURL, os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], body, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
