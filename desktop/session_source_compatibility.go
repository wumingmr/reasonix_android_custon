package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/fileutil"
	"reasonix/internal/session"
	"reasonix/internal/topicstate"
)

func desktopSourceKey(path, head string) string {
	// Migration sources include both transcript files and canonical prototype
	// directories. Keep their persisted key independent from the runtime
	// session locator, which intentionally accepts transcript paths only.
	pathKey := agent.CanonicalSessionPath(cleanDesktopPath(path))
	return agent.SessionSourceKeyFromIdentity(pathKey, head)
}

func (source desktopMigrationSource) mappingKey(path string) string {
	if source.registeredSourceKey != "" {
		return source.registeredSourceKey
	}
	key := desktopSourceKey(path, source.headID)
	if source.versionFingerprint != "" {
		key += ":review:" + source.versionFingerprint
	}
	return key
}

// Fingerprints cover source bytes, not the destination's evolving projection.
// A continued canonical session must never be replaced by its frozen import.
func desktopSourceFingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("session source is a symbolic link")
	}
	paths := []string{}
	if info.IsDir() {
		for _, name := range []string{"manifest.json", "header.json", "events.frames", "events.jsonl"} {
			candidate := filepath.Join(path, name)
			if _, err := os.Lstat(candidate); err == nil {
				paths = append(paths, candidate)
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
	} else {
		paths = append(paths, path)
		for _, artifact := range sessionTrashArtifacts(path, filepath.Base(path)) {
			if artifact.src == path {
				continue
			}
			// DAG selection lives in the event log. Branch .meta also contains
			// mutable catalog/title projections; hashing it would reject a source
			// simply because background indexing refreshed its display metadata.
			if strings.HasSuffix(artifact.name, ".events.jsonl") {
				if _, err := os.Lstat(artifact.src); err == nil {
					paths = append(paths, artifact.src)
				} else if !os.IsNotExist(err) {
					return "", err
				}
			}
		}
	}
	if len(paths) == 0 {
		return "", errors.New("session source has no durable records")
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, file := range paths {
		info, err := os.Lstat(file)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("session source contains a non-regular file")
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.Base(file), info.Size())
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *App) recordDesktopSource(ctx context.Context, path, format, fingerprint, targetID, workspaceID string) error {
	presentation := workspacestate.Presentation{SortOrder: -1, TopicID: legacySessionTopicID(path)}
	if meta, ok, err := agent.LoadBranchMeta(path); err == nil && ok {
		if meta.TopicID != "" {
			presentation.TopicID = meta.TopicID
		}
		presentation.Title = meta.TopicTitle
	}
	presentation = a.historicalTopicPresentation(ctx, workspaceID, presentation)
	return a.workspaceRegistry().RecordSource(ctx, workspacestate.SourceMapping{
		SourceKey: desktopSourceKey(path, ""), Path: path, Format: format, Fingerprint: fingerprint,
		SessionID: targetID, WorkspaceID: workspaceID,
	}, presentation)
}

func (a *App) commitDesktopImport(ctx context.Context, source desktopMigrationSource, path, format, fingerprint, targetID, workspaceID string) error {
	current, err := desktopSourceFingerprint(path)
	if err != nil {
		return err
	}
	if current != fingerprint {
		return workspacestate.ErrMutationConflict
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: targetID}
	if err := a.validateDesktopWorkspaceMembership(ctx, workspaceID, ref); err != nil {
		return err
	}
	if _, err := a.desktopSessionService("").Query().Snapshot(ctx, ref); err != nil {
		return err
	}
	key := source.mappingKey(path)
	mapping := workspacestate.SourceMapping{SourceKey: key, Path: path, HeadID: source.headID, Format: format, Fingerprint: fingerprint, SessionID: targetID, WorkspaceID: workspaceID}
	mapping.RetainedArtifacts, err = retainedDesktopArtifacts(path)
	if err != nil {
		return err
	}
	presentation := workspacestate.Presentation{SortOrder: -1}
	if format == "legacy" {
		presentation.TopicID = legacySessionTopicID(path)
	}
	if meta, ok, err := agent.LoadBranchMeta(path); err == nil && ok {
		if meta.TopicID != "" {
			presentation.TopicID = meta.TopicID
		}
		presentation.Title = meta.TopicTitle
	}
	presentation = a.historicalTopicPresentation(ctx, workspaceID, presentation)
	opID, err := a.prepareDesktopImport(ctx, source, path, fingerprint, targetID, workspaceID)
	if err != nil {
		return err
	}
	if err := a.workspaceRegistry().PrepareOperationContent(ctx, opID, []string{targetID}, &mapping, &presentation); err != nil {
		return err
	}
	if source.deferArchive {
		return nil
	}
	return a.workspaceRegistry().CommitOperation(ctx, opID)
}

func (a *App) historicalTopicPresentation(ctx context.Context, workspaceID string, presentation workspacestate.Presentation) workspacestate.Presentation {
	projects := loadProjectsFile()
	if workspaceID == workspacestate.GlobalWorkspaceID {
		return historicalTopicPresentationFrom(projects, workspaceID, "", presentation)
	}
	workspaceRoot := ""
	if state, err := a.workspaceRegistry().Load(ctx); err == nil {
		workspaceRoot = state.Workspaces[workspaceID].Root
	}
	return historicalTopicPresentationFrom(projects, workspaceID, workspaceRoot, presentation)
}

func historicalTopicPresentationFrom(projects desktopProjectFile, workspaceID, workspaceRoot string, presentation workspacestate.Presentation) workspacestate.Presentation {
	topics, pinned := projects.GlobalTopics, projects.GlobalPinnedTopics
	if workspaceID != workspacestate.GlobalWorkspaceID {
		topics, pinned = nil, nil
		for _, project := range projects.Projects {
			if (workspaceRoot != "" && sameProjectRoot(project.Root, workspaceRoot)) ||
				(workspaceRoot == "" && desktopWorkspaceID("project", project.Root) == workspaceID) {
				topics, pinned = project.Topics, project.PinnedTopics
				break
			}
		}
	}
	presentation.Pinned = containsDesktopString(pinned, presentation.TopicID)
	for rank, topic := range pinnedTopicIDs(topics, pinned) {
		if topic == presentation.TopicID {
			presentation.SortOrder = rank
			break
		}
	}
	return presentation
}

// These references are local recovery evidence, never telemetry. Directories
// retain their complete subtree; import success does not authorize cleanup.
func retainedDesktopArtifacts(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return []string{path}, nil
	}
	retained := []string{}
	for _, artifact := range sessionTrashArtifacts(path, filepath.Base(path)) {
		if _, err := os.Lstat(artifact.src); err == nil {
			retained = append(retained, artifact.src)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return retained, nil
}

// Reserve the destination before publishing content, so restart reconciliation
// cannot mistake an interrupted import for an ordinary unregistered session.
func (a *App) prepareDesktopImport(ctx context.Context, source desktopMigrationSource, path, fingerprint, targetID, workspaceID string) (string, error) {
	opID := source.operationID
	if opID == "" {
		opID = "import-" + desktopSourceKey(path, source.headID) + "-" + fingerprint
		if source.deferArchive {
			opID = "archive-" + opID
		}
		state, err := a.workspaceRegistry().Load(ctx)
		if err != nil {
			return "", err
		}
		if source.versionFingerprint != "" {
			// Older builds used the ordinary import ID for versioned mappings.
			// Resume that exact reservation when present, but do not collide
			// with an ordinary import of the same source fingerprint.
			previous, exists := state.PendingOperations[opID]
			if !exists || previous.Mapping == nil || previous.Mapping.SourceKey != source.mappingKey(path) {
				opID = "review-" + opID
			}
		}
		lifecycle := workspacestate.Active
		kind := "import"
		if source.deferArchive {
			kind, lifecycle = "archive-import", workspacestate.Archived
		}
		if previous, ok := state.SessionStates[targetID]; ok {
			lifecycle = previous.Lifecycle
		}
		mapping := &workspacestate.SourceMapping{SourceKey: source.mappingKey(path), Path: path, HeadID: source.headID, Fingerprint: fingerprint, SessionID: targetID, WorkspaceID: workspaceID}
		if err := a.workspaceRegistry().BeginOperation(ctx, workspacestate.Operation{ID: opID, Kind: kind, WorkspaceID: workspaceID, SessionIDs: []string{targetID}, Mapping: mapping, Lifecycle: lifecycle, ExpectedGeneration: state.Generation}); err != nil {
			return "", err
		}
	}
	format := "legacy"
	if info, err := os.Stat(path); err != nil {
		return "", err
	} else if info.IsDir() {
		format = "canonical"
	}
	mapping := &workspacestate.SourceMapping{SourceKey: source.mappingKey(path), Path: path, HeadID: source.headID, Format: format, Fingerprint: fingerprint, SessionID: targetID, WorkspaceID: workspaceID}
	return opID, a.workspaceRegistry().ReserveOperationTargets(ctx, opID, []string{targetID}, mapping)
}

func (a *App) resolveDesktopImportTarget(ctx context.Context, query *session.Query, preferredID, key, mappingKey, contentDigest, path, fingerprint string, heads ...string) (string, bool, error) {
	headID := ""
	if len(heads) > 0 {
		headID = heads[0]
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return "", false, err
	}
	for _, op := range state.PendingOperations {
		// Explicit source versions reserve their own mapping key. Recover the
		// exact reservation even when the imported manifest has no provenance.
		if op.Mapping == nil || !slices.Contains(state.SourceKeys(op.Mapping.SourceKey), mappingKey) || op.Mapping.Fingerprint != fingerprint || len(op.SessionIDs) != 1 {
			continue
		}
		id := op.SessionIDs[0]
		if state.SessionStates[id].Lifecycle == workspacestate.Deleted {
			// A completed import receipt can outlive purge. It is not a live
			// reservation and cannot authorize reusing that identity.
			if op.Phase == "committed" {
				continue
			}
			return "", false, workspacestate.ErrMutationConflict
		}
		digest, err := canonicalMigrationDigest(ctx, query, session.SessionRef{HostID: localDesktopHostID, SessionID: id})
		if errors.Is(err, session.ErrSessionNotFound) {
			return id, true, nil
		}
		if err != nil {
			return "", false, err
		}
		if digest != contentDigest {
			return "", false, workspacestate.ErrMutationConflict
		}
		return id, false, nil
	}
	remappedRetired := state.SessionStates[preferredID].Lifecycle == workspacestate.Deleted
	if remappedRetired {
		if err := a.proveRetiredImportOrigin(ctx, state, path, headID, preferredID); err != nil {
			return "", false, err
		}
		digest := sha256.Sum256([]byte(mappingKey + "\x00" + preferredID + "\x00" + contentDigest))
		preferredID = "migr-" + hex.EncodeToString(digest[:12])
	}
	id, needsImport, err := resolveMigrationTarget(ctx, query, preferredID, key, contentDigest, path, headID)
	if err != nil {
		return "", false, err
	}
	if state.SessionStates[id].Lifecycle == workspacestate.Deleted {
		return "", false, workspacestate.ErrMutationConflict
	}
	for _, op := range state.PendingOperations {
		if (remappedRetired || op.Kind == "archive-import") && op.Phase != "committed" && slices.Contains(op.SessionIDs, id) &&
			(op.Mapping == nil || op.Mapping.SourceKey != mappingKey || op.Mapping.Fingerprint != fingerprint) {
			return "", false, workspacestate.ErrMutationConflict
		}
	}
	return id, needsImport, nil
}

func (a *App) legacyCanonicalRef(ctx context.Context, path string) (session.SessionRef, bool, error) {
	snapshot, err := a.workspaceRegistry().VerifySnapshot(ctx)
	if err != nil {
		return session.SessionRef{}, false, err
	}
	mapping, adopted, err := snapshot.ResolveSource(desktopSourceKey(path, ""))
	if err != nil {
		return session.SessionRef{}, false, err
	}
	if !adopted {
		// DAG migration records each head separately. A path-only legacy tab
		// still refers to the selected head, not a new import of that path.
		hasHeads, err := snapshot.HasHeadSource(path)
		if err != nil {
			return session.SessionRef{}, false, err
		}
		if hasHeads {
			heads, err := agent.ListSessionHeads(path)
			if err != nil {
				return session.SessionRef{}, false, err
			}
			for _, head := range heads {
				if head.Selected && !head.Retired {
					mapping, adopted, err = snapshot.ResolveSource(desktopSourceKey(path, head.ID))
					if err != nil {
						return session.SessionRef{}, false, err
					}
					break
				}
			}
		}
	}
	if adopted {
		if snapshot.Session(mapping.SessionID).State.Lifecycle == workspacestate.Deleted {
			return session.SessionRef{}, true, session.ErrSessionNotFound
		}
		// Adoption is durable. Opening the new conversation must not hash or
		// depend on the retained source, which another CLI may still be using.
		return session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}, true, nil
	}
	a.mu.RLock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || tab.Ctrl == nil {
			continue
		}
		if _, runtime, exclusive := exclusiveSessionBinding(tab.Ctrl); exclusive {
			source := runtime.Session().Manifest().Source
			if source != nil && sessionRuntimeKey(source.Path) == sessionRuntimeKey(path) {
				ref := runtime.Ref()
				a.mu.RUnlock()
				return ref, true, nil
			}
		}
	}
	a.mu.RUnlock()
	return session.SessionRef{}, false, nil
}

// The source manifest remains untouched. Metadata backups are captured before
// any registry upgrade and are content-addressed so subsequent starts preserve
// every distinct pre-upgrade snapshot.
func (a *App) backupDesktopUpgradeMetadata(ctx context.Context) error {
	return backupDesktopUpgradeMetadataAt(ctx, a.workspaceRegistry().Path())
}

func backupDesktopUpgradeMetadataAt(ctx context.Context, registryPath string) error {
	dir := filepath.Join(desktopConfigDir(), "desktop", "upgrade-backups")
	paths := []string{
		registryPath, filepath.Join(desktopConfigDir(), desktopProjectsFile),
		filepath.Join(desktopConfigDir(), tabsFileName), desktopMigrationLedgerPath(),
	}
	roots := []string{""}
	for _, project := range loadProjectsFile().Projects {
		roots = append(roots, project.Root)
	}
	for _, root := range roots {
		for _, path := range legacyTopicPaths(root) {
			paths = append(paths, path)
		}
		databasePath := config.DesktopTopicStatePath(root)
		if _, err := os.Lstat(databasePath); err == nil {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			tmp, err := os.MkdirTemp(dir, ".topic-snapshot-")
			if err != nil {
				return err
			}
			snapshot := filepath.Join(tmp, "snapshot.sqlite")
			if err := topicstate.BackupExisting(ctx, databasePath, snapshot); err != nil {
				_ = os.RemoveAll(tmp)
				return err
			}
			body, readErr := os.ReadFile(snapshot)
			_ = os.RemoveAll(tmp)
			if readErr != nil {
				return readErr
			}
			digest := sha256.Sum256(body)
			dest := filepath.Join(dir, "topics-"+hex.EncodeToString(digest[:])+".sqlite")
			if saved, err := os.ReadFile(dest); err == nil {
				if sha256.Sum256(saved) != digest {
					return errors.New("topic backup integrity failed")
				}
			} else if !os.IsNotExist(err) {
				return err
			} else if err := fileutil.AtomicWriteFileStrict(dest, body, 0600); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		dest := filepath.Join(dir, filepath.Base(path)+"-"+hex.EncodeToString(digest[:])+".bak")
		if saved, err := os.ReadFile(dest); err == nil {
			if sha256.Sum256(saved) != digest {
				return errors.New("session upgrade backup integrity failed")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := fileutil.AtomicWriteFileStrict(dest, body, 0600); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) sourceRecovery(ctx context.Context, path, format, reason, scope, root string, heads ...string) error {
	headID := ""
	if len(heads) > 0 {
		headID = heads[0]
	}
	key := desktopSourceKey(path, headID)
	fingerprint, _ := desktopSourceFingerprint(path)
	return a.workspaceRegistry().RecordRecovery(ctx, workspacestate.RecoveryEntry{
		ID: desktopRecoveryID(key, fingerprint), SourceKey: key, Path: path, HeadID: headID, Format: format, Reason: reason,
		Status: "pending", Scope: scope, WorkspaceRoot: root, Fingerprint: fingerprint,
	})
}

// Preserve alternate DAG heads as independently addressable recovery choices.
// Importing one head never selects, retires or rewrites a head in the original.
func (a *App) discoverLegacyHeads(ctx context.Context, path, format, scope, root string) error {
	heads, err := agent.ListSessionHeads(path)
	if err != nil {
		return errors.Join(err, a.sourceRecovery(ctx, path, format, "head_scan_failed", scope, root))
	}
	var joined error
	for _, head := range heads {
		if err := ctx.Err(); err != nil {
			return err
		}
		if head.Selected || head.Retired {
			continue
		}
		joined = errors.Join(joined, a.sourceRecovery(ctx, path, format, "alternate_head", scope, root, head.ID))
	}
	return joined
}

func desktopRecoveryID(key, fingerprint string) string {
	return "legacy-" + key + "-" + fingerprint
}

// Read legacy ledger evidence without altering it or losing unknown fields.
