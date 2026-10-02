package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

type SessionLifecycleTarget struct {
	WorkspaceID     string              `json:"workspaceId,omitempty"`
	Ref             *session.SessionRef `json:"ref,omitempty"`
	RecoveryEntryID string              `json:"recoveryEntryId,omitempty"`
}
type SessionLifecycleRequest struct {
	OperationID        string                   `json:"operationId"`
	Action             string                   `json:"action"`
	Targets            []SessionLifecycleTarget `json:"targets"`
	ExpectedGeneration uint64                   `json:"expectedGeneration"`
}
type SessionLifecycleItem struct {
	Target      SessionLifecycleTarget `json:"target"`
	Ref         *session.SessionRef    `json:"ref,omitempty"`
	WorkspaceID string                 `json:"workspaceId"`
	Committed   bool                   `json:"committed"`
	ErrorCode   string                 `json:"errorCode,omitempty"`
	Retryable   bool                   `json:"retryable"`
}
type SessionLifecycleResult struct {
	OperationID string                 `json:"operationId"`
	Generation  uint64                 `json:"generation"`
	Committed   bool                   `json:"committed"`
	Items       []SessionLifecycleItem `json:"items"`
}

// Serializes duplicate RPC deliveries; persisted receipts handle restart.
var desktopLifecycleCommands sync.Mutex

func (a *App) ApplySessionLifecycle(req SessionLifecycleRequest) (SessionLifecycleResult, error) {
	desktopLifecycleCommands.Lock()
	defer desktopLifecycleCommands.Unlock()
	out := SessionLifecycleResult{OperationID: req.OperationID, Items: []SessionLifecycleItem{}}
	if err := validateLifecycleRequest(req); err != nil {
		return out, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(sum[:])
	ctx := a.bootContext()
	store := a.workspaceRegistry()
	state, err := store.Load(ctx)
	if err != nil {
		return out, err
	}
	key := "command-" + req.OperationID
	if old, ok := state.PendingOperations[key]; ok {
		if old.Kind != "command" || old.RequestFingerprint != fingerprint {
			return out, workspacestate.ErrMutationConflict
		}
		if len(old.Result) > 0 {
			if err := json.Unmarshal(old.Result, &out); err != nil {
				return out, err
			}
			out.Generation = old.ResultGeneration
		}
		if old.Phase == "committed" {
			return out, nil
		}
	} else {
		begin := store.BeginCommand
		if req.Action == "purge" {
			begin = store.BeginPurgeCommand
		}
		if err := begin(ctx, key, fingerprint, body, req.ExpectedGeneration); err != nil {
			return out, err
		}
	}
	previous := out.Items
	out.Items = []SessionLifecycleItem{}
	archiveErr := a.archiveLifecycleCommand(req, key)
	for i, target := range req.Targets {
		if i < len(previous) && (previous[i].Committed || !previous[i].Retryable) {
			out.Items = append(out.Items, previous[i])
			continue
		}
		item, err := a.applyLifecycleTarget(req, key, i, target, state, archiveErr)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	state, err = store.Load(ctx)
	if err != nil {
		return out, err
	}
	out.Generation = state.Generation + 1
	out.Committed = true
	final := true
	for _, item := range out.Items {
		out.Committed = out.Committed && item.Committed
		if !item.Committed && item.Retryable {
			final = false
		}
	}
	body, err = json.Marshal(out)
	if err != nil {
		return out, err
	}
	a.lifecycleCheckpoint("before-command-result")
	if err := store.SaveCommandResult(ctx, key, body, final); err != nil {
		return out, err
	}
	a.lifecycleCheckpoint("after-command-result")
	state, err = store.Load(ctx)
	if err != nil {
		return out, err
	}
	out.Generation = state.PendingOperations[key].ResultGeneration
	a.emitProjectTreeChanged()
	return out, nil
}

type TrashEntry struct {
	ID              string              `json:"id"`
	Ref             *session.SessionRef `json:"ref,omitempty"`
	RecoveryEntryID string              `json:"recoveryEntryId,omitempty"`
	Title           string              `json:"title"`
	WorkspaceID     string              `json:"workspaceId"`
	WorkspaceTitle  string              `json:"workspaceTitle"`
	ArchivedAt      int64               `json:"archivedAt"`
	Health          string              `json:"health"`
	OperationPhase  string              `json:"operationPhase,omitempty"`
	CanPreview      bool                `json:"canPreview"`
	CanRestore      bool                `json:"canRestore"`
	CanPurge        bool                `json:"canPurge"`
	CleanupBatchID  string              `json:"cleanupBatchId,omitempty"`
	CleanupKind     string              `json:"cleanupKind,omitempty"`
}
type TrashEntryPage struct {
	Items      []TrashEntry `json:"items"`
	Generation uint64       `json:"generation"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

func (a *App) ListTrashEntries(query, cursor string, limit int) (TrashEntryPage, error) {
	out := TrashEntryPage{Items: []TrashEntry{}}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return out, err
	}
	out.Generation = state.Generation
	start, err := decodeWorkspaceSessionCursor(cursor, state.Generation)
	if err != nil {
		return out, err
	}
	rows := []TrashEntry{}
	service := a.desktopSessionService("")
	cleanupState, _ := a.legacyCleanup.Load(a.bootContext())
	cleanupBySession := legacyCleanupArchivedSessions(cleanupState)
	for id, status := range state.SessionStates {
		op := state.PendingOperations["purge-"+id]
		purgeState := workspacestate.ClassifyPurge(state, id)
		pending := purgeState == workspacestate.PurgeTombstoned || purgeState == workspacestate.PurgeContentRemoved || purgeState == workspacestate.PurgeInvalid
		if status.Lifecycle != workspacestate.Archived && !pending {
			continue
		}
		ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
		row := TrashEntry{ID: id, Ref: &ref, Title: state.Presentation[id].Title, ArchivedAt: status.ArchivedAt, CanPurge: purgeState != workspacestate.PurgeInvalid, Health: "ready"}
		decorateLegacyCleanupTrashEntry(&row, cleanupState.BatchID, cleanupBySession[id])
		for wid, w := range state.Workspaces {
			if containsDesktopString(w.SessionIDs, id) {
				row.WorkspaceID = wid
				row.WorkspaceTitle = w.Title
				break
			}
		}
		if info, e := service.Query().Stat(a.bootContext(), ref); e == nil {
			if info.Title != "" {
				row.Title = info.Title
			}
			row.CanPreview = !pending
			row.CanRestore = !pending
		} else {
			row.Health = "unavailable"
		}
		if pending {
			row.OperationPhase = op.Phase
			row.Health = "purge_pending"
		}
		if row.Title == "" {
			row.Title = id
		}
		if strings.Contains(strings.ToLower(row.Title+"\n"+row.WorkspaceTitle), strings.ToLower(query)) {
			rows = append(rows, row)
		}
	}
	rows = append(rows, legacyCleanupTopicTrashEntries(cleanupState, state, query)...)
	rows = append(rows, topicRemovalTrashEntries(state, query)...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ArchivedAt != rows[j].ArchivedAt {
			return rows[i].ArchivedAt > rows[j].ArchivedAt
		}
		return rows[i].ID < rows[j].ID
	})
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if start > len(rows) {
		start = len(rows)
	}
	end := min(start+limit, len(rows))
	out.Items = append(out.Items, rows[start:end]...)
	if end < len(rows) {
		out.NextCursor = fmt.Sprintf("%d:%d", state.Generation, end)
	}
	return out, nil
}

func validateLifecycleRequest(req SessionLifecycleRequest) error {
	if strings.TrimSpace(req.OperationID) == "" || len(req.OperationID) > 200 || len(req.Targets) == 0 || len(req.Targets) > 1000 {
		return errors.New("invalid lifecycle request")
	}
	if req.Action != "archive" && req.Action != "restore" && req.Action != "purge" {
		return errors.New("invalid lifecycle action")
	}
	seen := map[string]bool{}
	for _, target := range req.Targets {
		if (target.Ref == nil) == (target.RecoveryEntryID == "") {
			return errors.New("exactly one session identity is required")
		}
		if target.Ref != nil {
			if err := validateLocalSessionRef(*target.Ref); err != nil {
				return err
			}
		} else if req.Action != "restore" && !(req.Action == "purge" && (strings.HasPrefix(target.RecoveryEntryID, "legacy-cleanup:") || strings.HasPrefix(target.RecoveryEntryID, "topic-removal:"))) {
			return errors.New("historical recovery entries can only be restored")
		}
		body, _ := json.Marshal(target)
		if seen[string(body)] {
			return errors.New("duplicate lifecycle target")
		}
		seen[string(body)] = true
	}
	return nil
}

func (a *App) archiveLifecycleCommand(req SessionLifecycleRequest, key string) error {
	store := a.workspaceRegistry()
	if req.Action == "archive" {
		child := key + "-archive"
		state, err := store.Load(a.bootContext())
		if err != nil {
			return err
		}
		if state.PendingOperations[child].Phase != "committed" {
			for _, target := range req.Targets {
				if state.SessionStates[target.Ref.SessionID].Generation > req.ExpectedGeneration {
					return workspacestate.ErrMutationConflict
				}
			}
			refs := []session.SessionRef{}
			for _, target := range req.Targets {
				refs = append(refs, *target.Ref)
			}
			release := a.lockRuntimeMutation("archive lifecycle command")
			e := a.archiveSessionRefsWithOperation(refs, child)
			release()
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func (a *App) applyLifecycleTarget(req SessionLifecycleRequest, key string, index int, target SessionLifecycleTarget, state workspacestate.State, archiveErr error) (SessionLifecycleItem, error) {
	ctx, store := a.bootContext(), a.workspaceRegistry()
	item := SessionLifecycleItem{Target: target, Ref: target.Ref}
	var opErr error
	child := fmt.Sprintf("%s-%d", key, index)
	latest, loadErr := store.Load(ctx)
	if loadErr != nil {
		return item, loadErr
	}
	if target.Ref != nil && req.Action != "archive" && latest.PendingOperations[child].Phase != "committed" && latest.SessionStates[target.Ref.SessionID].Generation > req.ExpectedGeneration {
		purgeState := workspacestate.ClassifyPurge(latest, target.Ref.SessionID)
		resumingPurge := req.Action == "purge" && (purgeState == workspacestate.PurgeTombstoned || purgeState == workspacestate.PurgeContentRemoved || purgeState == workspacestate.PurgeCommitted)
		if !resumingPurge {
			item.ErrorCode = "state_conflict"
			return item, nil
		}
	}
	if target.Ref != nil {
		for id, w := range state.Workspaces {
			if containsDesktopString(w.SessionIDs, target.Ref.SessionID) {
				item.WorkspaceID = id
				break
			}
		}
	}
	switch req.Action {
	case "archive":
		opErr = archiveErr
	case "purge":
		if target.Ref == nil && strings.HasPrefix(target.RecoveryEntryID, "topic-removal:") {
			opErr = a.purgeRemovedTopic(strings.TrimPrefix(target.RecoveryEntryID, "topic-removal:"), target.WorkspaceID)
			break
		}
		if target.Ref == nil && strings.HasPrefix(target.RecoveryEntryID, "legacy-cleanup:") {
			opErr = a.purgeLegacyCleanupTopic(strings.TrimPrefix(target.RecoveryEntryID, "legacy-cleanup:"), target.WorkspaceID)
		} else {
			release := a.lockRuntimeMutation("purge lifecycle command")
			opErr = a.purgeCanonicalSession(ctx, *target.Ref, req.ExpectedGeneration)
			release()
		}
	case "restore":
		if target.Ref == nil && strings.HasPrefix(target.RecoveryEntryID, "topic-removal:") {
			opErr = a.restoreRemovedTopic(strings.TrimPrefix(target.RecoveryEntryID, "topic-removal:"), target.WorkspaceID)
			item.WorkspaceID = target.WorkspaceID
			break
		}
		if target.Ref == nil && strings.HasPrefix(target.RecoveryEntryID, "legacy-cleanup:") {
			opErr = a.restoreLegacyCleanupTopic(strings.TrimPrefix(target.RecoveryEntryID, "legacy-cleanup:"), target.WorkspaceID)
			item.WorkspaceID = target.WorkspaceID
			break
		}
		var restored SessionRestoreResult
		if target.Ref == nil {
			restored, opErr = a.restoreRecoveryEntryInWorkspace(target.RecoveryEntryID, child, target.WorkspaceID)
		} else {
			release := a.lockRuntimeMutation("restore lifecycle command")
			saved, loadErr := store.Load(ctx)
			if loadErr != nil {
				opErr = loadErr
			} else if done := saved.PendingOperations[child]; done.Phase == "committed" {
				restored = SessionRestoreResult{Session: *target.Ref, WorkspaceID: done.WorkspaceID, Generation: done.ResultGeneration}
			} else {
				restored, opErr = a.restoreCanonicalSession(ctx, *target.Ref, child)
			}
			release()
		}
		if opErr == nil {
			item.Ref = &restored.Session
			item.WorkspaceID = restored.WorkspaceID
		}
	}
	item.Committed = opErr == nil
	if opErr != nil {
		item.ErrorCode = "operation_failed"
		item.Retryable = true
		if errors.Is(opErr, workspacestate.ErrMutationConflict) {
			item.ErrorCode = "state_conflict"
			item.Retryable = false
		}
	}
	return item, nil
}
