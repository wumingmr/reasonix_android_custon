package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/topicstate"
)

func topicRemovalTrashEntries(state workspacestate.State, query string) []TrashEntry {
	rows := []TrashEntry{}
	for _, operation := range state.TopicRemovals {
		if operation.Disposition != "archive_placeholder" || (operation.Phase != "committed" && operation.Phase != "prepared" && operation.Phase != "restoring") {
			continue
		}
		var item legacycleanup.Candidate
		if json.Unmarshal(operation.Snapshot, &item) != nil || item.Topic == nil {
			continue
		}
		workspaceTitle := state.Workspaces[item.WorkspaceID].Title
		if workspaceTitle == "" {
			workspaceTitle = globalProjectTitle()
			if item.Topic.Scope == "project" {
				workspaceTitle = workspaceName(item.Topic.WorkspaceRoot)
			}
		}
		if !strings.Contains(strings.ToLower(item.Title+"\n"+workspaceTitle), strings.ToLower(query)) {
			continue
		}
		ready := operation.Phase == "committed"
		row := TrashEntry{ID: "topic-removal:" + operation.ID, RecoveryEntryID: "topic-removal:" + operation.ID, Title: item.Title,
			WorkspaceID: item.WorkspaceID, WorkspaceTitle: workspaceTitle, ArchivedAt: operation.ArchivedAt,
			CanRestore: ready, CanPurge: ready, Health: "ready"}
		if !ready {
			row.Health, row.OperationPhase = "operation_pending", operation.Phase
		}
		rows = append(rows, row)
	}
	return rows
}

// Only explicit user removals are replayed here, never a new cleanup batch.
func (a *App) reconcileTopicRemovals(state workspacestate.State) error {
	var joined error
	for _, operation := range state.TopicRemovals {
		switch operation.Phase {
		case "prepared":
			out, err := a.RemoveTopic(TopicRemovalRequest{OperationID: operation.ID, Target: TopicRemovalTarget{WorkspaceID: operation.WorkspaceID, TopicID: operation.TopicID}, ExpectedToken: operation.Token})
			if err == nil {
				err = topicRemovalError(out)
			}
			joined = errors.Join(joined, err)
		case "restoring":
			joined = errors.Join(joined, a.restoreRemovedTopic(operation.ID, operation.WorkspaceID))
		}
	}
	return joined
}

func (a *App) purgeRemovedTopic(id, workspaceID string) error {
	return a.workspaceRegistry().TransitionTopicRemoval(a.bootContext(), id, func(_ workspacestate.State, operation *workspacestate.TopicRemoval, _ func() error) error {
		if operation.ID != id || operation.WorkspaceID != workspaceID || operation.Disposition != "archive_placeholder" {
			return workspacestate.ErrMutationConflict
		}
		if operation.Phase == "purged" {
			return nil
		}
		if operation.Phase != "committed" {
			return workspacestate.ErrMutationConflict
		}
		operation.Phase, operation.Snapshot = "purged", nil
		operation.Metadata = nil
		return nil
	})
}

func (a *App) restoreRemovedTopic(id, workspaceID string) error {
	release, ok := a.tryLockRuntimeMutation("restore topic placeholder")
	if !ok {
		return errTopicArchiveBusy
	}
	defer release()
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	return a.workspaceRegistry().TransitionTopicRemoval(a.bootContext(), id, func(state workspacestate.State, operation *workspacestate.TopicRemoval, checkpoint func() error) error {
		if operation.ID != id || operation.WorkspaceID != workspaceID || operation.Disposition != "archive_placeholder" {
			return workspacestate.ErrMutationConflict
		}
		if operation.Phase == "restored" {
			return nil
		}
		if operation.Phase != "committed" && operation.Phase != "restoring" {
			return workspacestate.ErrMutationConflict
		}
		var item legacycleanup.Candidate
		if err := json.Unmarshal(operation.Snapshot, &item); err != nil {
			return err
		}
		if item.Topic == nil {
			return workspacestate.ErrMutationConflict
		}
		var metadata topicstate.Record
		if err := json.Unmarshal(operation.Metadata, &metadata); err != nil {
			return err
		}
		if err := a.checkRemovedTopicSources(state, item); err != nil {
			return err
		}
		desktopProjectsFileMu.Lock()
		defer desktopProjectsFileMu.Unlock()
		release, err := acquireDesktopProjectsFileLock()
		if err != nil {
			return err
		}
		defer release()
		file, err := readTopicRemovalProjects()
		if err != nil {
			return err
		}
		if topicRemovalHasOtherOwner(file, item) {
			return workspacestate.ErrMutationConflict
		}
		if item.Topic.Scope == "project" && projectIndexByRoot(file.Projects, item.Topic.WorkspaceRoot) < 0 {
			return workspacestate.ErrWorkspaceNotFound
		}
		return desktopTopicState.withExclusiveScope(topicTitleRoot(item.Topic.Scope, item.Topic.WorkspaceRoot), func(ctx context.Context, store *topicstate.Store) error {
			return a.restoreRemovedTopicMetadataLocked(ctx, store, state, file, item, metadata, operation, checkpoint)
		})
	})
}

func restoreRemovedTopicIndex(file *desktopProjectFile, item legacycleanup.Candidate) {
	snapshot := item.Topic
	file.DeletedTopics = removeString(file.DeletedTopics, item.TopicID)
	if snapshot.Scope == "global" {
		file.GlobalTopics = insertLegacyTopicAt(file.GlobalTopics, item.TopicID, snapshot.Order)
		if snapshot.Pinned && !slices.Contains(file.GlobalPinnedTopics, item.TopicID) {
			file.GlobalPinnedTopics = append(file.GlobalPinnedTopics, item.TopicID)
		}
		if groups, changed := restoreLegacyTopicGroup(file.GlobalGroups, snapshot, item.TopicID); changed {
			file.GlobalGroups = groups
			file.GlobalGroupsRevision++
		}
		return
	}
	i := projectIndexByRoot(file.Projects, snapshot.WorkspaceRoot)
	project := &file.Projects[i]
	project.Topics = insertLegacyTopicAt(project.Topics, item.TopicID, snapshot.Order)
	if snapshot.Pinned && !slices.Contains(project.PinnedTopics, item.TopicID) {
		project.PinnedTopics = append(project.PinnedTopics, item.TopicID)
	}
	if groups, changed := restoreLegacyTopicGroup(project.Groups, snapshot, item.TopicID); changed {
		project.Groups = groups
		project.GroupsRevision++
	}
}

// Requires registry, projects and topic-store writer ownership.
func (a *App) restoreRemovedTopicMetadataLocked(ctx context.Context, store *topicstate.Store, state workspacestate.State, file desktopProjectFile, item legacycleanup.Candidate, metadata topicstate.Record, operation *workspacestate.TopicRemoval, checkpoint func() error) error {
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return err
	}
	record, exists := snapshot.Records[item.TopicID]
	indexed := topicIndexedInProjectsSnapshot(file, item.Topic.Scope, item.Topic.WorkspaceRoot, item.TopicID)
	if operation.Phase == "committed" {
		if exists || indexed || !slices.Contains(file.DeletedTopics, item.TopicID) {
			return workspacestate.ErrMutationConflict
		}
		operation.Phase, operation.RestoreRevision = "restoring", snapshot.State.Revision+1
		if err := checkpoint(); err != nil {
			return err
		}
	} else if exists {
		if record.RowRevision != operation.RestoreRevision || record.Title != item.Title || record.TitleSource != item.Topic.TitleSource || record.CreatedAtMS != item.Topic.CreatedAt {
			return workspacestate.ErrMutationConflict
		}
	} else if indexed {
		return workspacestate.ErrMutationConflict
	}
	if !exists {
		// Another topic may have advanced the scope revision while this restore
		// was interrupted before its first write. No target row exists yet, so
		// reserve the new revision durably; an existing row still uses the CAS above.
		if operation.RestoreRevision != snapshot.State.Revision+1 {
			operation.RestoreRevision = snapshot.State.Revision + 1
			if err := checkpoint(); err != nil {
				return err
			}
		}
		a.lifecycleCheckpoint("topic-restore-before-metadata")
		if _, err := store.Update(ctx, item.TopicID, func(record *topicstate.Record) {
			*record = metadata
		}); err != nil {
			return err
		}
	}
	if indexed {
		// Do not overwrite organization edits made after an interrupted restore.
		current, err := topicRemovalCandidate(state, file, TopicRemovalTarget{WorkspaceID: item.WorkspaceID, TopicID: item.TopicID})
		if err != nil {
			return err
		}
		if current.Topic.Pinned != item.Topic.Pinned {
			return workspacestate.ErrMutationConflict
		}
	} else {
		a.lifecycleCheckpoint("topic-restore-before-index")
		restoreRemovedTopicIndex(&file, item)
		if err := saveProjectsFile(file); err != nil {
			return err
		}
	}
	a.lifecycleCheckpoint("topic-restore-before-commit")
	operation.Phase = "restored"
	return nil
}
