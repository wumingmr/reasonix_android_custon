package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/store"
	"reasonix/internal/topicstate"
)

func (a *App) processLegacyCleanupTopic(item legacycleanup.Candidate) {
	if item.Topic == nil || !isDefaultTopicTitle(item.Title) {
		a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
			next.Phase, next.Classification, next.Reason = "protected", "protected", "invalid_topic_snapshot"
		})
		return
	}
	if a.reconcileLegacyCleanupTopicArchive(item) {
		return
	}
	releaseRuntime, ok := a.tryLockRuntimeMutation("legacy empty topic cleanup")
	if !ok {
		a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
			next.Phase, next.Classification, next.Reason = "busy", "busy", "runtime_mutation"
		})
		return
	}
	defer releaseRuntime()
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	classification, reason := a.classifyLegacyCleanupTopicLocked(item)
	if classification != "empty" {
		a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
			next.Phase, next.Classification, next.Reason = classification, classification, reason
		})
		return
	}
	if a.legacyCleanupWorker.beforeArchive != nil {
		a.legacyCleanupWorker.beforeArchive()
	}
	a.archiveLegacyCleanupTopic(item)
}

func (a *App) archiveLegacyCleanupTopic(item legacycleanup.Candidate) {
	classification, reason := "unknown", "topic_archive_failed"
	archived := false
	_, err := a.legacyCleanup.Transition(a.bootContext(), func(state *legacycleanup.State) error {
		current, ok := state.Items[item.ID]
		if !ok || current.Restored || current.Kind != "topic" || current.Topic == nil ||
			current.Phase == "archived" || current.Phase == "protected" || current.Phase == "has_content" {
			return errLegacyCleanupStateChanged
		}
		current.Phase, current.Classification, current.Reason = "archive_pending", "empty", ""
		state.Items[item.ID] = current
		item = current
		return nil
	}, func() error {
		return a.workspaceRegistry().WithStateLocked(a.bootContext(), func(workspaces workspacestate.State) error {
			desktopProjectsFileMu.Lock()
			defer desktopProjectsFileMu.Unlock()
			releaseProjects, lockErr := acquireDesktopProjectsFileLock()
			if lockErr != nil {
				return lockErr
			}
			defer releaseProjects()
			projects := loadProjectsFile()
			root := topicTitleRoot(item.Topic.Scope, item.Topic.WorkspaceRoot)
			return desktopTopicState.withExclusiveScope(root, func(ctx context.Context, topicStore *topicstate.Store) error {
				snapshot, snapshotErr := topicStore.Snapshot(ctx)
				if snapshotErr != nil {
					return snapshotErr
				}
				classification, reason = a.classifyLegacyCleanupTopicSnapshotLocked(item, workspaces, projects, snapshot)
				if classification != "empty" {
					return nil
				}
				if removeErr := removeTopicFromProjectsFileCrossProcessLocked(item.TopicID); removeErr != nil {
					return removeErr
				}
				if _, deleteErr := topicStore.Delete(ctx, item.TopicID); deleteErr != nil {
					return deleteErr
				}
				archived = true
				return nil
			})
		})
	}, func(state *legacycleanup.State, effectErr error) error {
		current := state.Items[item.ID]
		if effectErr != nil {
			current.Phase, current.Classification, current.Reason = "unknown", "unknown", "topic_archive_failed"
		} else if archived {
			current.Phase, current.Classification, current.Reason, current.ArchivedAt = "archived", "empty", "", time.Now().UTC().UnixMilli()
		} else {
			current.Phase, current.Classification, current.Reason = classification, classification, reason
		}
		state.Items[item.ID] = current
		return nil
	})
	if err != nil && !errors.Is(err, errLegacyCleanupStateChanged) {
		slog.Warn("desktop: legacy topic cleanup transition failed", "err", err)
	}
}

func (a *App) markLegacyCleanupTopicArchivePending(id string) bool {
	_, err := a.legacyCleanup.Update(a.bootContext(), func(state *legacycleanup.State) error {
		item, ok := state.Items[id]
		if !ok || item.Restored || item.Kind != "topic" || item.Phase == "archived" || item.Phase == "protected" || item.Phase == "has_content" {
			return errLegacyCleanupStateChanged
		}
		item.Phase, item.Classification, item.Reason = "archive_pending", "empty", ""
		state.Items[id] = item
		return nil
	})
	if err != nil {
		if !errors.Is(err, errLegacyCleanupStateChanged) {
			slog.Warn("desktop: legacy cleanup archive marker failed", "err", err)
		}
		return false
	}
	return true
}

func (a *App) reconcileLegacyCleanupTopicArchive(item legacycleanup.Candidate) bool {
	if item.Phase != "archive_pending" || item.Topic == nil {
		return false
	}
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	desktopProjectsFileMu.Lock()
	defer desktopProjectsFileMu.Unlock()
	releaseProjects, err := acquireDesktopProjectsFileLock()
	if err != nil {
		return false
	}
	defer releaseProjects()
	file := loadProjectsFile()
	if !containsDesktopString(file.DeletedTopics, item.TopicID) || topicIndexedInRegistry(item.Topic.Scope, item.Topic.WorkspaceRoot, item.TopicID) {
		return false
	}
	a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
		if next.Restored || next.Phase != "archive_pending" {
			return
		}
		next.Phase, next.Classification, next.Reason = "archived", "empty", ""
		if next.ArchivedAt == 0 {
			next.ArchivedAt = time.Now().UTC().UnixMilli()
		}
	})
	return true
}

// classifyLegacyCleanupTopicLocked requires runtime mutation admission,
// topicTitleMutationMu and topicIndexMu. It performs no mutation and never
// creates a Session or Controller.
func (a *App) classifyLegacyCleanupTopicLocked(item legacycleanup.Candidate) (string, string) {
	cleanupState, err := a.legacyCleanup.Load(a.bootContext())
	if err != nil {
		return "unknown", "cleanup_state_unavailable"
	}
	current, ok := cleanupState.Items[item.ID]
	if !ok || current.Restored || current.Phase == "archived" || current.Topic == nil {
		return "protected", "cleanup_state_changed"
	}
	if current.Phase == "protected" || current.Phase == "has_content" {
		classification := current.Classification
		if classification == "" {
			classification = current.Phase
		}
		return classification, current.Reason
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return "unknown", "workspace_unavailable"
	}
	projects := loadProjectsFile()
	snapshot, err := desktopTopicState.snapshot(topicTitleRoot(item.Topic.Scope, item.Topic.WorkspaceRoot))
	if err != nil {
		return "unknown", "topic_metadata_unavailable"
	}
	return a.classifyLegacyCleanupTopicSnapshotLocked(current, state, projects, snapshot)
}

func (a *App) classifyLegacyCleanupTopicSnapshotLocked(item legacycleanup.Candidate, state workspacestate.State, projects desktopProjectFile, snapshot topicstate.Snapshot) (string, string) {
	for id, presentation := range state.Presentation {
		if presentation.TopicID == item.TopicID && state.SessionStates[id].Lifecycle != workspacestate.Deleted {
			return "protected", "canonical_session_present"
		}
	}
	if classification, reason := classifyLegacyCleanupTopicSources(item, a.knownSessionDirs()); classification != "empty" {
		return classification, reason
	}
	a.mu.RLock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.TopicID == item.TopicID {
			a.mu.RUnlock()
			return "busy", "topic_open"
		}
	}
	a.mu.RUnlock()
	record, ok := snapshot.Records[item.TopicID]
	if !ok || agent.UserPreviewText(record.Title) != item.Title || !topicIndexedInProjectsSnapshot(projects, item.Topic.Scope, item.Topic.WorkspaceRoot, item.TopicID) {
		return "protected", "topic_changed"
	}
	if item.Topic.RowRevision == 0 {
		return "unknown", "topic_version_unavailable"
	}
	if record.RowRevision != item.Topic.RowRevision {
		return "protected", "title_mutated"
	}
	if !legacyCleanupTopicSnapshotMatchesProjects(item, projects) {
		return "protected", "topic_organization_changed"
	}
	return "empty", ""
}

func classifyLegacyCleanupTopicSources(item legacycleanup.Candidate, knownDirs []string) (string, string) {
	if item.Topic == nil {
		return "protected", "invalid_topic_snapshot"
	}
	if item.Topic.Scope == "project" {
		info, err := os.Stat(item.Topic.WorkspaceRoot)
		if err != nil || !info.IsDir() {
			return "unknown", "workspace_unavailable"
		}
	}
	for _, dir := range uniqueStrings(knownDirs) {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "unknown", "legacy_session_index_unavailable"
		}
		for _, entry := range entries {
			if entry.IsDir() || !store.IsSessionTranscriptName(entry.Name()) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			meta, ok, err := agent.LoadBranchMeta(path)
			if err != nil || !ok {
				// Without readable metadata there is no reliable way to prove that
				// this historical transcript belongs to a different Topic.
				return "unknown", "legacy_session_metadata_unavailable"
			}
			match := topicSessionMatch{path: path, scope: meta.DefaultScope(), workspaceRoot: meta.WorkspaceRoot}
			if strings.TrimSpace(meta.TopicID) != item.TopicID || !topicSessionMatchMatchesTarget(match, item.Topic.Scope, item.Topic.WorkspaceRoot) {
				continue
			}
			if agent.IsCleanupPending(path) {
				return "protected", "legacy_cleanup_pending"
			}
			return "protected", "legacy_session_appeared"
		}
	}
	return "empty", ""
}

func topicIndexedInProjectsSnapshot(file desktopProjectFile, scope, workspaceRoot, topicID string) bool {
	if scope == "global" {
		return containsDesktopString(file.GlobalTopics, topicID)
	}
	index := projectIndexByRoot(file.Projects, workspaceRoot)
	return index >= 0 && containsDesktopString(file.Projects[index].Topics, topicID)
}

func legacyCleanupTopicSnapshotMatchesProjects(item legacycleanup.Candidate, file desktopProjectFile) bool {
	if item.Topic == nil {
		return false
	}
	var topics, pinned []string
	var groups []desktopGroup
	if item.Topic.Scope == "global" {
		topics, pinned, groups = file.GlobalTopics, file.GlobalPinnedTopics, file.GlobalGroups
	} else {
		index := projectIndexByRoot(file.Projects, item.Topic.WorkspaceRoot)
		if index < 0 {
			return false
		}
		topics, pinned, groups = file.Projects[index].Topics, file.Projects[index].PinnedTopics, file.Projects[index].Groups
	}
	if slices.Index(topics, item.TopicID) != item.Topic.Order || containsDesktopString(pinned, item.TopicID) != item.Topic.Pinned {
		return false
	}
	groupID, groupOrder := "", -1
	for _, group := range groups {
		if index := slices.Index(group.TopicIDs, item.TopicID); index >= 0 {
			groupID, groupOrder = group.ID, index
			break
		}
	}
	return groupID == item.Topic.GroupID && groupOrder == item.Topic.GroupOrder
}

func (a *App) protectLegacyCleanupTopicMutation(topicID string) {
	if a == nil || a.legacyCleanup == nil || strings.TrimSpace(topicID) == "" {
		return
	}
	_, err := a.legacyCleanup.Update(a.bootContext(), func(state *legacycleanup.State) error {
		for id, item := range state.Items {
			if item.Kind != "topic" || item.TopicID != topicID || item.Restored || item.Phase == "archived" {
				continue
			}
			item.Phase, item.Classification, item.Reason = "protected", "protected", "title_mutated"
			state.Items[id] = item
		}
		return nil
	})
	if err != nil && !errors.Is(err, legacycleanup.ErrNotInitialized) {
		slog.Warn("desktop: legacy cleanup title mutation marker failed", "err", err)
	}
}

func (a *App) restoreLegacyCleanupTopic(id, expectedWorkspaceID string) error {
	state, err := a.legacyCleanup.Load(a.bootContext())
	if err != nil {
		return err
	}
	item, ok := state.Items[id]
	if !ok || item.Kind != "topic" || item.Topic == nil || item.WorkspaceID != expectedWorkspaceID {
		return errors.New("legacy cleanup topic entry is unavailable")
	}
	if item.Restored && item.Phase == "restored" {
		return nil
	}
	if item.Phase != "archived" {
		return errors.New("legacy cleanup topic entry is unavailable")
	}
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	if err := restoreLegacyCleanupTopicLocked(item); err != nil {
		return err
	}
	_, err = a.legacyCleanup.Update(a.bootContext(), func(next *legacycleanup.State) error {
		current := next.Items[id]
		if current.Phase != "archived" {
			return errLegacyCleanupStateChanged
		}
		current.Phase, current.Classification, current.Reason, current.Restored = "restored", "protected", "restored_by_user", true
		next.Items[id] = current
		return nil
	})
	if err == nil {
		a.emitProjectTreeChanged()
	}
	return err
}

func (a *App) purgeLegacyCleanupTopic(id, expectedWorkspaceID string) error {
	_, err := a.legacyCleanup.Update(a.bootContext(), func(state *legacycleanup.State) error {
		item, ok := state.Items[id]
		if !ok || item.Kind != "topic" || item.WorkspaceID != expectedWorkspaceID {
			return errors.New("legacy cleanup topic entry is unavailable")
		}
		if item.Phase == "purged" && item.Restored {
			return nil
		}
		if item.Phase != "archived" || item.Restored {
			return errLegacyCleanupStateChanged
		}
		item.Phase, item.Classification, item.Reason = "purged", "protected", "purged_by_user"
		item.Restored = true
		item.Topic = nil
		state.Items[id] = item
		return nil
	})
	return err
}

func insertLegacyTopicAt(items []string, id string, order int) []string {
	items = removeString(items, id)
	if order < 0 || order > len(items) {
		order = len(items)
	}
	items = append(items, "")
	copy(items[order+1:], items[order:])
	items[order] = id
	return items
}

func restoreLegacyTopicGroup(groups []desktopGroup, snapshot *legacycleanup.TopicSnapshot, topicID string) ([]desktopGroup, bool) {
	if snapshot == nil || snapshot.GroupID == "" {
		return groups, false
	}
	for i := range groups {
		if groups[i].ID != snapshot.GroupID {
			continue
		}
		next := insertLegacyTopicAt(groups[i].TopicIDs, topicID, snapshot.GroupOrder)
		if sameStringList(next, groups[i].TopicIDs) {
			return groups, false
		}
		groups[i].TopicIDs = next
		return groups, true
	}
	return groups, false
}

// restoreLegacyCleanupTopicLocked requires topicTitleMutationMu followed by
// topicIndexMu. It merges one frozen placeholder into the current project file
// and deliberately leaves unrelated concurrent entries untouched.
func restoreLegacyCleanupTopicLocked(item legacycleanup.Candidate) error {
	snapshot := item.Topic
	if snapshot == nil {
		return errors.New("legacy cleanup topic snapshot is unavailable")
	}
	titles, err := loadTopicTitlesForUpdate(topicTitleRoot(snapshot.Scope, snapshot.WorkspaceRoot))
	if err != nil {
		return err
	}
	alreadyIndexed := topicIndexedInRegistry(snapshot.Scope, snapshot.WorkspaceRoot, item.TopicID)
	if alreadyIndexed && titles[item.TopicID] != item.Title {
		return errLegacyCleanupStateChanged
	}
	if !alreadyIndexed {
		if snapshot.CreatedAt > 0 {
			err = createTopicState(snapshot.WorkspaceRoot, item.TopicID, item.Title, snapshot.TitleSource, snapshot.CreatedAt)
		} else {
			err = setTopicTitleWithSource(snapshot.WorkspaceRoot, item.TopicID, item.Title, snapshot.TitleSource)
		}
		if err != nil {
			return err
		}
	}
	err = updateProjectsFile(func(file *desktopProjectFile) (bool, error) {
		changed := false
		if next := removeString(file.DeletedTopics, item.TopicID); !sameStringList(next, file.DeletedTopics) {
			file.DeletedTopics = next
			changed = true
		}
		if snapshot.Scope == "global" {
			next := insertLegacyTopicAt(file.GlobalTopics, item.TopicID, snapshot.Order)
			if !sameStringList(next, file.GlobalTopics) {
				file.GlobalTopics = next
				changed = true
			}
			if snapshot.Pinned && !containsDesktopString(file.GlobalPinnedTopics, item.TopicID) {
				file.GlobalPinnedTopics = append(file.GlobalPinnedTopics, item.TopicID)
				changed = true
			}
			if groups, groupChanged := restoreLegacyTopicGroup(file.GlobalGroups, snapshot, item.TopicID); groupChanged {
				file.GlobalGroups = groups
				file.GlobalGroupsRevision++
				changed = true
			}
			return changed, nil
		}
		index := projectIndexByRoot(file.Projects, snapshot.WorkspaceRoot)
		if index < 0 {
			return false, errors.New("legacy cleanup workspace is no longer registered")
		}
		project := &file.Projects[index]
		next := insertLegacyTopicAt(project.Topics, item.TopicID, snapshot.Order)
		if !sameStringList(next, project.Topics) {
			project.Topics = next
			changed = true
		}
		if snapshot.Pinned && !containsDesktopString(project.PinnedTopics, item.TopicID) {
			project.PinnedTopics = append(project.PinnedTopics, item.TopicID)
			changed = true
		}
		if groups, groupChanged := restoreLegacyTopicGroup(project.Groups, snapshot, item.TopicID); groupChanged {
			project.Groups = groups
			project.GroupsRevision++
			changed = true
		}
		return changed, nil
	})
	if err != nil && !alreadyIndexed {
		_ = deleteTopicState(snapshot.WorkspaceRoot, item.TopicID)
	}
	return err
}

func (a *App) markLegacyCleanupSessionRestored(sessionID string) {
	if a == nil || a.legacyCleanup == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	_, err := a.legacyCleanup.Update(a.bootContext(), func(state *legacycleanup.State) error {
		for id, item := range state.Items {
			if item.SessionID != sessionID || item.Phase != "archived" {
				continue
			}
			item.Phase, item.Classification, item.Reason, item.Restored = "restored", "protected", "restored_by_user", true
			state.Items[id] = item
		}
		return nil
	})
	if err != nil && !errors.Is(err, legacycleanup.ErrNotInitialized) {
		slog.Warn("desktop: legacy cleanup restore marker failed", "err", err)
	}
}
