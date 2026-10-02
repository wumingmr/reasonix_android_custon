package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

// A conversion is linked by persisted provenance, never by title or a coincident
// ID. LegacyDir identifies the immutable original snapshot of an unnamed head.
type desktopMigrationConversion struct {
	extra     map[string]json.RawMessage
	Root      string `json:"root"`
	SessionID string `json:"sessionId"`
	HeadID    string `json:"headId,omitempty"`
	LegacyDir string `json:"legacyDir,omitempty"`
	Codec     string `json:"codec"`
	Depth     int    `json:"depth"`
}

type desktopMigrationStoredSource struct {
	dir      string
	manifest session.Manifest
}

func discoverDesktopMigrationConversions(ctx context.Context, sources map[string]*desktopMigrationSource) (map[string][]desktopMigrationConversion, map[string]bool, error) {
	nodes := map[string]desktopMigrationStoredSource{}
	var joined error
	for _, source := range sources {
		entries, err := os.ReadDir(source.root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			dir := filepath.Join(source.root, entry.Name())
			manifest, err := readDesktopMigrationManifest(dir)
			// Normal source migration reports damaged/unsupported stores. A bad
			// manifest cannot authorize suppressing another source.
			if err != nil || manifest.SessionID != entry.Name() {
				continue
			}
			nodes[canonicalRuntimeRoot(dir)] = desktopMigrationStoredSource{dir: dir, manifest: manifest}
		}
	}
	var origin func(desktopMigrationStoredSource, map[string]bool) (string, string, string, int, bool)
	origin = func(node desktopMigrationStoredSource, seen map[string]bool) (string, string, string, int, bool) {
		key := canonicalRuntimeRoot(node.dir)
		if seen[key] || len(seen) >= 32 {
			return "", "", "", 0, false
		}
		if node.manifest.Source == nil {
			return key, "", "", 0, true
		}
		seen[key] = true
		provenance := node.manifest.Source
		if store.IsSessionTranscriptName(filepath.Base(provenance.Path)) && (provenance.Version == "" || provenance.Version == "legacy") {
			return canonicalRuntimeRoot(provenance.Path), provenance.LegacyHeadID, filepath.Join(node.dir, "legacy"), 1, true
		}
		parent, found := nodes[canonicalRuntimeRoot(provenance.Path)]
		if !found {
			// Preview conversion preserves the old manifest inside its archive,
			// so ancestry remains available after removal of an intermediate store.
			dir := filepath.Join(node.dir, "legacy", "prototype")
			manifest, err := readDesktopMigrationManifest(dir)
			if err != nil {
				return "", "", "", 0, false
			}
			parent = desktopMigrationStoredSource{dir: dir, manifest: manifest}
		}
		path, head, legacyDir, depth, ok := origin(parent, seen)
		if ok && parent.manifest.Source == nil {
			// Separate conversions archive the same native source in different
			// directories. Keep its persisted identity after the original is
			// removed; the archive location is not a new conversation origin.
			path = canonicalRuntimeRoot(provenance.Path)
		}
		return path, head, legacyDir, depth + 1, ok
	}
	hidden := desktopMigrationRecoveryStores(nodes)
	result := map[string][]desktopMigrationConversion{}
	for key, node := range nodes {
		if hidden[key] {
			continue
		}
		path, head, legacyDir, depth, ok := origin(node, map[string]bool{})
		if !ok {
			continue
		}
		result[path] = append(result[path], desktopMigrationConversion{Root: filepath.Dir(node.dir), SessionID: filepath.Base(node.dir), HeadID: head, LegacyDir: legacyDir, Codec: node.manifest.Codec, Depth: depth})
	}
	for path := range result {
		sort.Slice(result[path], func(i, j int) bool {
			a, b := result[path][i], result[path][j]
			if a.Depth != b.Depth {
				return a.Depth > b.Depth
			}
			return filepath.Join(a.Root, a.SessionID) < filepath.Join(b.Root, b.SessionID)
		})
	}
	return result, hidden, joined
}

func readDesktopMigrationManifest(dir string) (session.Manifest, error) {
	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return session.Manifest{}, err
	}
	var manifest session.Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return manifest, err
	}
	if manifest.Codec != session.Codec && manifest.Codec != session.PrototypeCodec && manifest.Codec != session.LegacyLinearCodec && manifest.Codec != session.FinalV31Codec {
		return manifest, session.ErrUnsupportedVersion
	}
	return manifest, nil
}

func desktopLegacyMigrationFiles(path string, source desktopMigrationSource) []string {
	files := desktopLegacySourceFiles(path, source.pairedRoot)
	for _, converted := range source.conversions[canonicalRuntimeRoot(path)] {
		files = append(files, canonicalMigrationSourceFiles(converted.Root, converted.SessionID)...)
		files = append(files, legacyMigrationSourceFiles(filepath.Join(converted.LegacyDir, filepath.Base(path)))...)
	}
	return files
}

func resolveDesktopConversionHeads(ctx context.Context, path string, candidates []desktopMigrationConversion) ([]desktopMigrationConversion, error) {
	resolved := append([]desktopMigrationConversion(nil), candidates...)
	for index := range resolved {
		converted := &resolved[index]
		if converted.HeadID != "" || converted.LegacyDir == "" {
			continue
		}
		// An omitted head means the selected head at conversion time, not the
		// source's current selection. Read the preserved snapshot to recover it.
		frozenPath := filepath.Join(converted.LegacyDir, filepath.Base(path))
		heads, err := session.LegacyMigrationHeads(ctx, frozenPath)
		if err != nil {
			return nil, err
		}
		for _, head := range heads {
			if head.Selected && !head.Retired {
				converted.HeadID = head.ID
				break
			}
		}
	}
	return resolved, nil
}

type desktopMigrationStagedNode struct {
	service    *session.Service
	checkpoint desktopMigrationCheckpoint
	ref        session.SessionRef
	messages   []provider.Message
	digest     string
	targetID   string
	coveredBy  int
	origin     session.SessionOrigin
}

// All generations of one legacy head are staged before publication. Maximal
// histories survive; equal/prefix ancestors get receipts for the same target.
// Incomparable continuations remain separate conversations.
func (a *App) migrateConversionLineage(ctx context.Context, path, headID string, source desktopMigrationSource, cp *desktopMigrationCheckpoint, workspaceID string) (retErr error) {
	tmp, err := os.MkdirTemp("", "reasonix-lineage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	stages := []*session.Service{}
	defer func() {
		for _, stage := range stages {
			retErr = errors.Join(retErr, stage.Shutdown(context.Background()))
		}
	}()
	newStage := func() (*session.Service, error) {
		// A continued descendant can have the same ID/source stamp as the
		// import of its ancestor. Isolate inputs so reuse cannot substitute a
		// descendant's newer history while computing the ancestor's digest.
		dir, err := os.MkdirTemp(tmp, "source-")
		if err != nil {
			return nil, err
		}
		stage, err := session.NewService("migration-stage", session.NewFilesystemPersistence(filepath.Join(dir, "sessions-v4")))
		if err == nil {
			stages = append(stages, stage)
		}
		return stage, err
	}
	nodes := []desktopMigrationStagedNode{}
	add := func(stage *session.Service, ref session.SessionRef, checkpoint desktopMigrationCheckpoint, origin session.SessionOrigin) error {
		if err := stage.Close(ctx, ref); err != nil {
			return err
		}
		messages, err := stage.Query().History(ctx, ref)
		if err != nil {
			return err
		}
		digest, err := agent.ContentDigestForMessages(messages)
		if err != nil {
			return err
		}
		nodes = append(nodes, desktopMigrationStagedNode{service: stage, checkpoint: checkpoint, ref: ref, messages: messages, digest: digest, coveredBy: -1, origin: origin})
		return nil
	}
	for _, converted := range source.headConversions {
		key := desktopCanonicalMigrationKey(converted.Root, converted.SessionID)
		checkpoint, err := newDesktopMigrationCheckpoint(source, key, canonicalMigrationSourceFiles(converted.Root, converted.SessionID))
		if err != nil {
			return err
		}
		stage, err := newStage()
		if err != nil {
			return err
		}
		var runtime *session.Runtime
		if converted.Codec == session.Codec {
			runtime, _, err = stage.ContinueImportedSource(ctx, path, filepath.Join(converted.Root, converted.SessionID), headID)
		} else {
			runtime, _, err = stage.ContinuePrototype(ctx, filepath.Join(converted.Root, converted.SessionID))
		}
		if err != nil {
			return errors.Join(err, updateDesktopMigrationLedger(key, "", "failed", "conversion_read"))
		}
		if err := add(stage, runtime.Ref(), checkpoint, session.SessionOriginCanonicalImport); err != nil {
			return err
		}
	}
	if cp != nil {
		stage, err := newStage()
		if err != nil {
			return err
		}
		runtime, _, err := stage.ContinueImported(ctx, path, headID)
		if err != nil {
			return err
		}
		if err := add(stage, runtime.Ref(), *cp, session.SessionOriginLegacyImport); err != nil {
			return err
		}
	}
	for _, node := range nodes {
		if node.checkpoint.completed() {
			source.conversionAdoptions = append(source.conversionAdoptions, &desktopMigrationReceipt{TargetSessionID: node.checkpoint.record.TargetSessionID, ContentDigest: node.checkpoint.record.ContentDigest})
		}
	}
	reduceDesktopMigrationLineage(nodes)
	for i := range nodes {
		if nodes[i].coveredBy >= 0 {
			continue
		}
		if err := a.publishStagedMigration(ctx, source, nodes[i].checkpoint, nodes[i].service, nodes[i].ref, workspaceID, nodes[i].origin); err != nil {
			return err
		}
		ledger, err := readDesktopMigrationLedger()
		if err != nil {
			return err
		}
		nodes[i].targetID = ledger.Records[nodes[i].checkpoint.key].TargetSessionID
	}
	for i := range nodes {
		if nodes[i].coveredBy < 0 {
			continue
		}
		winner := nodes[i].coveredBy
		for nodes[winner].coveredBy >= 0 {
			winner = nodes[winner].coveredBy
		}
		// Preserve an older receipt for a target that may already have been
		// continued/deleted. Do not detach or resurrect that user's target.
		targetID := nodes[winner].targetID
		if nodes[i].checkpoint.matchesCompletedContent(nodes[i].digest) {
			targetID = nodes[i].checkpoint.record.TargetSessionID
		}
		if err := a.completeRegisteredMigration(ctx, source, nodes[i].checkpoint, targetID, nodes[i].digest); err != nil {
			return err
		}
	}
	return nil
}

// Select maximal histories within an already-proven lineage. Equal histories
// prefer the earlier (more recently converted) node; prefix edges cannot cycle.
func reduceDesktopMigrationLineage(nodes []desktopMigrationStagedNode) {
	for i := range nodes {
		for j := range nodes {
			if i == j || !session.MigrationHistoryContains(nodes[j].messages, nodes[i].messages) {
				continue
			}
			if session.MigrationHistoryContains(nodes[i].messages, nodes[j].messages) && j > i {
				continue
			}
			nodes[i].coveredBy = j
			break
		}
	}
}

// Stored-only chains (for example native v3 -> v4 with both directories left
// behind) use the same reduction even when no JSONL source remains on disk.
func (a *App) migrateStoredConversionLineages(ctx context.Context, conversions map[string][]desktopMigrationConversion, sources map[string]*desktopMigrationSource, handled map[string]bool) error {
	var joined error
	for path, candidates := range conversions {
		remaining := []desktopMigrationConversion{}
		for _, candidate := range candidates {
			if handled[canonicalRuntimeRoot(filepath.Join(candidate.Root, candidate.SessionID))] {
				continue
			}
			owner := sources[canonicalRuntimeRoot(candidate.Root)]
			if owner != nil && owner.pairedIDs[candidate.SessionID] {
				continue
			}
			remaining = append(remaining, candidate)
		}
		if len(remaining) < 2 {
			continue
		}
		// Head recovery can require replaying an archived DAG. Completed
		// stored-only chains must bypass that work just like live legacy files.
		ledger, err := readDesktopMigrationLedger()
		if err != nil {
			return errors.Join(joined, err)
		}
		allUnchanged := true
		for _, candidate := range remaining {
			cp, err := newDesktopMigrationCheckpoint(desktopMigrationSource{records: ledger.Records}, desktopCanonicalMigrationKey(candidate.Root, candidate.SessionID), canonicalMigrationSourceFiles(candidate.Root, candidate.SessionID))
			if err != nil || !cp.unchanged() {
				allUnchanged = false
				break
			}
			if err := cp.skip(); err != nil {
				joined = errors.Join(joined, err)
				allUnchanged = false
				break
			}
		}
		if allUnchanged {
			for _, candidate := range remaining {
				handled[canonicalRuntimeRoot(filepath.Join(candidate.Root, candidate.SessionID))] = true
			}
			continue
		}
		resolved, err := resolveDesktopConversionHeads(ctx, path, remaining)
		if err != nil {
			joined = errors.Join(joined, err)
			for _, candidate := range remaining {
				handled[canonicalRuntimeRoot(filepath.Join(candidate.Root, candidate.SessionID))] = true
				joined = errors.Join(joined, updateDesktopMigrationLedger(desktopCanonicalMigrationKey(candidate.Root, candidate.SessionID), "", "failed", "conversion_heads"))
			}
			continue
		}
		groups := map[string][]desktopMigrationConversion{}
		for _, candidate := range resolved {
			groups[candidate.HeadID] = append(groups[candidate.HeadID], candidate)
		}
		for _, group := range groups {
			if len(group) < 2 {
				continue
			}
			owner := sources[canonicalRuntimeRoot(group[0].Root)]
			if owner == nil {
				continue
			}
			source := *owner
			source.headConversions = group
			ledger, err := readDesktopMigrationLedger()
			if err != nil {
				return errors.Join(joined, err)
			}
			source.records = ledger.Records
			unchanged := true
			for _, candidate := range group {
				handled[canonicalRuntimeRoot(filepath.Join(candidate.Root, candidate.SessionID))] = true
				cp, err := newDesktopMigrationCheckpoint(source, desktopCanonicalMigrationKey(candidate.Root, candidate.SessionID), canonicalMigrationSourceFiles(candidate.Root, candidate.SessionID))
				if err != nil {
					joined = errors.Join(joined, err)
					unchanged = false
					continue
				}
				if !cp.unchanged() {
					unchanged = false
				} else if err := cp.skip(); err != nil {
					joined = errors.Join(joined, err)
					unchanged = false
				}
			}
			if unchanged {
				continue
			}
			if err := a.migrateConversionLineage(ctx, "", "", source, nil, ""); err != nil {
				joined = errors.Join(joined, err)
			}
		}
	}
	return joined
}
