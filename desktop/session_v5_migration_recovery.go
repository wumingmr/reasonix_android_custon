package main

import (
	"path/filepath"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/store"
)

// Recovery copies are durable safety artifacts, not ordinary conversations.
// Classification uses the historical writer's identity markers, never titles,
// text equality or the mere presence of a parent (which also marks user forks).
// Missing display metadata must still allow ordinary history to migrate.
func desktopMigrationAutomaticRecovery(path string) bool {
	if agent.LooksLikeRecoveryFilename(path) {
		return true
	}
	meta, found, err := agent.LoadBranchMeta(path)
	return err == nil && found && (meta.Recovered || meta.EffectiveVersionKind() == agent.VersionRecovery || strings.TrimSpace(meta.RecoveryDigest) != "")
}

// Carry the exclusion through paired stores and conversion ancestry. Original
// files can be gone, so consult the immutable archived metadata as well. This
// only controls discovery; it never deletes or rewrites any recovery artifact.
func desktopMigrationRecoveryStores(nodes map[string]desktopMigrationStoredSource) map[string]bool {
	hidden := map[string]bool{}
	var recovery func(desktopMigrationStoredSource, map[string]bool) bool
	recovery = func(node desktopMigrationStoredSource, seen map[string]bool) bool {
		key := canonicalRuntimeRoot(node.dir)
		if seen[key] || len(seen) >= 32 {
			return false
		}
		seen[key] = true
		if agent.LooksLikeRecoveryFilename(node.manifest.SessionID+".jsonl") ||
			desktopMigrationAutomaticRecovery(filepath.Join(filepath.Dir(filepath.Dir(node.dir)), "sessions", node.manifest.SessionID+".jsonl")) {
			return true
		}
		source := node.manifest.Source
		if source == nil {
			return false
		}
		if store.IsSessionTranscriptName(filepath.Base(source.Path)) && (source.Version == "" || source.Version == "legacy") {
			return desktopMigrationAutomaticRecovery(source.Path) ||
				desktopMigrationAutomaticRecovery(filepath.Join(node.dir, "legacy", filepath.Base(source.Path)))
		}
		if parent, found := nodes[canonicalRuntimeRoot(source.Path)]; found {
			return recovery(parent, seen)
		}
		dir := filepath.Join(node.dir, "legacy", "prototype")
		manifest, err := readDesktopMigrationManifest(dir)
		return err == nil && recovery(desktopMigrationStoredSource{dir: dir, manifest: manifest}, seen)
	}
	for key, node := range nodes {
		if recovery(node, map[string]bool{}) {
			hidden[key] = true
		}
	}
	return hidden
}
