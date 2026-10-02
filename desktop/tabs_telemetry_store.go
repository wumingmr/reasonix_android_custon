package main

import (
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/session"
)

const canonicalTelemetryDir = "session-telemetry"

// canonicalTelemetryPath keeps desktop telemetry out of the session service's
// own by-id directory, which that service owns exclusively.
func canonicalTelemetryPath(sessionID string) string {
	id := strings.TrimSpace(sessionID)
	if session.ValidateSessionID(id) != nil {
		return ""
	}
	dir := config.MemoryUserDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, canonicalTelemetryDir, id+".telemetry.json")
}

// telemetryFilePath resolves a session identity (legacy path or canonical
// route) to its telemetry sidecar, or "" when none can be named.
func telemetryFilePath(identity string) string {
	if identity == "" {
		return ""
	}
	if id, ok := parseSessionRoute(identity); ok {
		return canonicalTelemetryPath(id)
	}
	return identity + ".telemetry.json"
}

func saveTelemetryFor(identity string, snapshot tabTelemetrySnapshot) error {
	path := telemetryFilePath(identity)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return saveTelemetry(path, snapshot)
}

func loadTelemetryFor(identity string) tabTelemetrySnapshot {
	return loadTelemetry(telemetryFilePath(identity))
}
