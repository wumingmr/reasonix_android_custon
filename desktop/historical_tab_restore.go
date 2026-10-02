package main

import (
	"os"
	"path/filepath"

	"reasonix/internal/agent"
	"reasonix/internal/store"
)

// A source proven by background discovery is a pending user choice, not a
// damaged canonical session. Never read its content or acquire its writer lock
// while restoring presentation. Durable recovery/mapping evidence wins.
func (a *App) savedTabHistoricalSource(entry desktopTabEntry, evidence savedTabReconcileEvidence) *SessionSourceRef {
	if entry.SessionID != "" || entry.SessionPath == "" || evidence.registryErr != nil {
		return nil
	}
	if _, found, _ := savedTabPendingSessionIdentity(entry, evidence); found {
		return nil
	}
	if savedTabHasRecoveryOwner(entry, evidence) {
		return nil
	}
	path := agent.CanonicalSessionPath(entry.SessionPath)
	key := desktopSourceKey(path, entry.SessionHeadID)
	if _, adopted, err := historicalMappingForSource(evidence.registry, key); adopted || err != nil {
		return nil
	}
	// The saved identity is already known. Stat just this source, without
	// waiting for (or iterating over) the background discovery map.
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	valid := info.Mode().IsRegular() && store.IsSessionTranscriptName(filepath.Base(path))
	if info.IsDir() {
		valid = hasHistoricalSessionArtifacts(path)
	}
	if valid {
		return &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: entry.SessionHeadID, SourceKey: key}
	}
	return nil
}

func hasHistoricalSessionArtifacts(path string) bool {
	for _, name := range []string{"manifest.json", "events.frames", "events.jsonl"} {
		if info, err := os.Lstat(filepath.Join(path, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}
