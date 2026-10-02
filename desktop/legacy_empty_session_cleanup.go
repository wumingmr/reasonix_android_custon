package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/filelock"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

const legacyEmptySessionCleanupEvent = "legacy-empty-session-cleanup:changed"

var errLegacyCleanupStateChanged = errors.New("legacy cleanup candidate changed after inspection")

type LegacyEmptySessionCleanupItem struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	WorkspaceID    string `json:"workspaceId,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	TopicID        string `json:"topicId,omitempty"`
	Title          string `json:"title,omitempty"`
	Phase          string `json:"phase"`
	Classification string `json:"classification,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type LegacyEmptySessionCleanupStatus struct {
	Version    int                             `json:"version"`
	BatchID    string                          `json:"batchId,omitempty"`
	State      string                          `json:"state"`
	Removed    int                             `json:"removed"`
	Pending    int                             `json:"pending"`
	Busy       int                             `json:"busy"`
	Unknown    int                             `json:"unknown"`
	Protected  int                             `json:"protected"`
	HasContent int                             `json:"hasContent"`
	Items      []LegacyEmptySessionCleanupItem `json:"items"`
}

type legacyCleanupDecision struct {
	classification string
	reason         string
	info           session.SessionInfo
	snapshot       session.Snapshot
}

type legacyCleanupWorkerState struct {
	mu      sync.Mutex
	running bool
	// beforeArchive is a deterministic race-test hook. Set before cleanup and
	// never mutate it concurrently.
	beforeArchive func()
}

func legacyCleanupBatchID(state workspacestate.State) string {
	ids := make([]string, 0, len(state.SessionStates))
	for id := range state.SessionStates {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	body, _ := json.Marshal(struct {
		Generation uint64
		IDs        []string
		At         int64
	}{state.Generation, ids, time.Now().UTC().UnixNano()})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:12])
}

func legacyCleanupSourcePaths(sessionPath string) ([]string, error) {
	paths := make([]string, 0, 24)
	for _, artifact := range sessionTrashArtifacts(sessionPath, filepath.Base(sessionPath)) {
		paths = append(paths, artifact.src)
	}
	subagents, err := agent.ListSubagentsByParent(filepath.Dir(sessionPath), agent.BranchID(sessionPath))
	if err != nil {
		return nil, err
	}
	for _, artifact := range subagents {
		paths = append(paths, artifact.SessionPath, artifact.MetaPath)
		paths = append(paths, store.SessionSidecarFiles(artifact.SessionPath)...)
		paths = append(paths,
			store.SessionCheckpointDir(artifact.SessionPath),
			store.SessionJobsDir(artifact.SessionPath),
			store.SessionInboxDir(artifact.SessionPath),
		)
	}
	paths = uniqueStrings(paths)
	slices.Sort(paths)
	return paths, nil
}

// legacyCleanupSourceFingerprint covers every durable artifact that can make a
// legacy session recoverable. It deliberately excludes lease and lock files.
func legacyCleanupSourceFingerprint(sessionPath string) (string, error) {
	paths, err := legacyCleanupSourcePaths(sessionPath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	found := false
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("legacy cleanup source contains a symbolic link")
		}
		found = true
		root := filepath.Dir(sessionPath)
		if info.IsDir() {
			err = filepath.WalkDir(path, func(child string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				childInfo, infoErr := entry.Info()
				if infoErr != nil {
					return infoErr
				}
				if childInfo.Mode()&os.ModeSymlink != 0 {
					return errors.New("legacy cleanup source contains a symbolic link")
				}
				rel, relErr := filepath.Rel(root, child)
				if relErr != nil {
					return relErr
				}
				fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), childInfo.Size())
				if entry.IsDir() {
					return nil
				}
				if !childInfo.Mode().IsRegular() {
					return errors.New("legacy cleanup source contains a non-regular file")
				}
				file, openErr := os.Open(child)
				if openErr != nil {
					return openErr
				}
				_, copyErr := io.Copy(h, file)
				closeErr := file.Close()
				return errors.Join(copyErr, closeErr)
			})
			if err != nil {
				return "", err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("legacy cleanup source contains a non-regular file")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), info.Size())
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(h, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", err
		}
	}
	if !found {
		return "", os.ErrNotExist
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// initializeLegacyEmptySessionCleanupBatch freezes identities before the
// renderer can create a new draft-backed session. Content inspection remains a
// background operation after migration and draft recovery.
func (a *App) initializeLegacyEmptySessionCleanupBatch() error {
	if a == nil || a.legacyCleanup == nil {
		return errors.New("legacy cleanup store is unavailable")
	}
	if _, err := a.legacyCleanup.Load(a.bootContext()); err == nil {
		return nil
	} else if !errors.Is(err, legacycleanup.ErrNotInitialized) {
		return err
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return err
	}
	builder := newLegacyCleanupBatchBuilder(a, state)
	builder.registerCanonicalSessions(a.bootContext())
	projects := loadProjectsFile()
	builder.registerTopics("global", "", workspacestate.GlobalWorkspaceID, projects.GlobalTopics, projects.GlobalPinnedTopics, projects.GlobalGroups)
	for _, project := range projects.Projects {
		workspaceID := builder.workspaceIDForRoot(project.Root)
		if workspaceID != "" {
			builder.registerTopics("project", project.Root, workspaceID, project.Topics, project.PinnedTopics, project.Groups)
		}
	}
	_, _, err = a.legacyCleanup.Initialize(a.bootContext(), legacycleanup.State{
		BatchID: legacyCleanupBatchID(state),
		Items:   builder.items,
	})
	return err
}

func (a *App) GetLegacyEmptySessionCleanupStatus() (LegacyEmptySessionCleanupStatus, error) {
	state, err := a.legacyCleanup.Load(a.bootContext())
	if errors.Is(err, legacycleanup.ErrNotInitialized) {
		return LegacyEmptySessionCleanupStatus{Version: 1, State: "not_initialized", Items: []LegacyEmptySessionCleanupItem{}}, nil
	}
	if err != nil {
		return LegacyEmptySessionCleanupStatus{}, err
	}
	return legacyCleanupStatus(state), nil
}

func legacyCleanupStatus(state legacycleanup.State) LegacyEmptySessionCleanupStatus {
	out := LegacyEmptySessionCleanupStatus{Version: state.Version, BatchID: state.BatchID, State: "complete", Items: []LegacyEmptySessionCleanupItem{}}
	for _, item := range legacycleanup.SortedItems(state) {
		out.Items = append(out.Items, LegacyEmptySessionCleanupItem{ID: item.ID, Kind: item.Kind, WorkspaceID: item.WorkspaceID, SessionID: item.SessionID, TopicID: item.TopicID, Title: item.Title, Phase: item.Phase, Classification: item.Classification, Reason: item.Reason})
		switch item.Phase {
		case "archived":
			out.Removed++
		case "busy":
			out.Busy++
			out.Pending++
		case "unknown", "registered", "verified", "archive_pending":
			out.Unknown++
			out.Pending++
		case "protected", "restored":
			out.Protected++
		case "has_content":
			out.HasContent++
		}
	}
	if out.Pending > 0 {
		out.State = "pending"
	}
	return out
}

func (a *App) RetryLegacyEmptySessionCleanup() (LegacyEmptySessionCleanupStatus, error) {
	status, err := a.GetLegacyEmptySessionCleanupStatus()
	if err != nil {
		return status, err
	}
	return status, errors.New("automatic empty conversation cleanup has been retired; existing trash entries can still be restored")
}

func (a *App) runLegacyEmptySessionCleanup(includeUnknown bool) {
	if a == nil || a.legacyCleanup == nil {
		return
	}
	a.legacyCleanupWorker.mu.Lock()
	if a.legacyCleanupWorker.running {
		a.legacyCleanupWorker.mu.Unlock()
		return
	}
	a.legacyCleanupWorker.running = true
	a.legacyCleanupWorker.mu.Unlock()
	defer func() {
		a.legacyCleanupWorker.mu.Lock()
		a.legacyCleanupWorker.running = false
		a.legacyCleanupWorker.mu.Unlock()
	}()
	releaseWorker, err := a.legacyCleanup.TryAcquireWorker()
	if err != nil {
		if !errors.Is(err, filelock.ErrHeld) {
			slog.Warn("desktop: legacy empty session cleanup worker unavailable", "err", err)
		}
		return
	}
	defer releaseWorker()
	if a.desktopMigrationDone != nil {
		select {
		case <-a.desktopMigrationDone:
		case <-a.bootContext().Done():
			return
		}
	}
	a.mu.RLock()
	tabsRestored := a.tabsRestored
	a.mu.RUnlock()
	if tabsRestored != nil {
		select {
		case <-tabsRestored:
		case <-a.bootContext().Done():
			return
		}
	}
	state, err := a.legacyCleanup.Load(a.bootContext())
	if err != nil {
		if !errors.Is(err, legacycleanup.ErrNotInitialized) {
			slog.Warn("desktop: legacy empty session cleanup disabled", "err", err)
		}
		return
	}
	before := legacyCleanupStatus(state).Removed
	for _, item := range legacycleanup.SortedItems(state) {
		if item.Restored || item.Phase == "archived" || item.Phase == "has_content" || item.Phase == "protected" {
			continue
		}
		if item.Phase == "unknown" && !includeUnknown {
			continue
		}
		switch item.Kind {
		case "session":
			a.processLegacyCleanupSession(item)
		case "legacy":
			a.processLegacyCleanupSource(item)
		case "topic":
			a.processLegacyCleanupTopic(item)
		}
	}
	after, err := a.GetLegacyEmptySessionCleanupStatus()
	if err == nil {
		a.emitRuntimeEvent(legacyEmptySessionCleanupEvent, after)
		if after.Removed > before {
			a.emitProjectTreeChanged()
		}
	}
}

func (a *App) reconcileLegacyCleanupArchivedOperation(item legacycleanup.Candidate, sessionID string) bool {
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return false
	}
	op, ok := state.PendingOperations[item.OperationID]
	if !ok || op.Kind != "archive" || op.Phase != "committed" || !slices.Contains(op.SessionIDs, sessionID) ||
		state.SessionStates[sessionID].Lifecycle != workspacestate.Archived {
		return false
	}
	a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
		next.SessionID = sessionID
		next.Phase, next.Classification, next.Reason = "archived", "empty", ""
		if next.ArchivedAt == 0 {
			next.ArchivedAt = state.SessionStates[sessionID].ArchivedAt
			if next.ArchivedAt == 0 {
				next.ArchivedAt = time.Now().UTC().UnixMilli()
			}
		}
	})
	return true
}

func (a *App) updateLegacyCleanupItem(id string, update func(*legacycleanup.Candidate)) {
	_, err := a.legacyCleanup.Update(a.bootContext(), func(state *legacycleanup.State) error {
		item, ok := state.Items[id]
		if !ok || item.Restored {
			return errLegacyCleanupStateChanged
		}
		update(&item)
		state.Items[id] = item
		return nil
	})
	if err != nil && !errors.Is(err, errLegacyCleanupStateChanged) {
		slog.Warn("desktop: legacy cleanup state update failed", "err", err)
	}
}

func (a *App) processLegacyCleanupSession(item legacycleanup.Candidate) {
	if a.reconcileLegacyCleanupArchivedOperation(item, item.SessionID) {
		return
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: item.SessionID}
	decision := a.classifyLegacyCleanupSession(a.bootContext(), ref, item)
	if decision.classification != "empty" {
		phase := decision.classification
		if phase == "empty" || phase == "" {
			phase = "unknown"
		}
		a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
			next.Phase, next.Classification, next.Reason = phase, decision.classification, decision.reason
		})
		return
	}
	if a.legacyCleanupWorker.beforeArchive != nil {
		a.legacyCleanupWorker.beforeArchive()
	}
	release, ok := a.tryLockRuntimeMutation("legacy empty session cleanup")
	if !ok {
		a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
			next.Phase, next.Classification, next.Reason = "busy", "busy", "runtime_mutation"
		})
		return
	}
	defer release()
	verify := func(ctx context.Context, latest workspacestate.State) error {
		fresh := a.classifyLegacyCleanupSession(ctx, ref, item)
		if fresh.classification != "empty" {
			return fmt.Errorf("%w: %s", errLegacyCleanupStateChanged, fresh.classification)
		}
		return nil
	}
	err := a.archiveSessionRefsWithOperationConditional([]session.SessionRef{ref}, item.OperationID, verify)
	if err != nil {
		classification, reason := "unknown", "archive_failed"
		if errors.Is(err, errTopicHasActiveWork) || errors.Is(err, errTopicArchiveBusy) {
			classification, reason = "busy", "runtime_active"
		} else if errors.Is(err, errLegacyCleanupStateChanged) || errors.Is(err, workspacestate.ErrMutationConflict) {
			classification, reason = "protected", "state_changed"
		}
		a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
			next.Phase, next.Classification, next.Reason = classification, classification, reason
		})
		return
	}
	a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
		next.Phase, next.Classification, next.Reason, next.ArchivedAt = "archived", "empty", "", time.Now().UTC().UnixMilli()
	})
}
