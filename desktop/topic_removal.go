package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/topicstate"
)

type TopicRemovalTarget struct {
	WorkspaceID string `json:"workspaceId"`
	TopicID     string `json:"topicId"`
}
type TopicRemovalInspection struct {
	Target      TopicRemovalTarget `json:"target"`
	Disposition string             `json:"disposition"`
	Allowed     bool               `json:"allowed"`
	Reason      string             `json:"reason,omitempty"`
	Token       string             `json:"token"`
}
type TopicRemovalRequest struct {
	OperationID   string             `json:"operationId"`
	Target        TopicRemovalTarget `json:"target"`
	ExpectedToken string             `json:"expectedToken"`
}
type TopicRemovalResult struct {
	Committed       bool   `json:"committed"`
	Disposition     string `json:"disposition"`
	RecoveryEntryID string `json:"recoveryEntryId,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	Retryable       bool   `json:"retryable"`
}

// Removal must not inherit the display reader's best-effort empty fallback.
func readTopicRemovalProjects() (desktopProjectFile, error) {
	var file desktopProjectFile
	body, err := readFileUTF8(filepath.Join(desktopConfigDir(), desktopProjectsFile))
	if err != nil && !os.IsNotExist(err) {
		return file, err
	}
	if err == nil {
		if err = json.Unmarshal(body, &file); err != nil {
			return file, err
		}
	}
	file = normalizeProjectsFile(file)
	body, err = readFileUTF8(filepath.Join(desktopConfigDir(), desktopProjectOrganizationFile))
	if os.IsNotExist(err) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	var organization desktopProjectOrganizationFileData
	if err := json.Unmarshal(body, &organization); err != nil {
		return file, err
	}
	if organization.Version < 1 || organization.Version > desktopProjectOrganizationVersion {
		return file, errors.New("unsupported topic organization version")
	}
	return applyProjectOrganization(file, organization), nil
}

func topicRemovalWorkspaceID(state workspacestate.State, scope, root string) string {
	for id, workspace := range state.Workspaces {
		if sameDesktopPath(workspace.Root, desktopWorkspaceRoot(scope, root)) {
			return id
		}
	}
	return desktopWorkspaceID(scope, root)
}

func topicRemovalCandidate(state workspacestate.State, file desktopProjectFile, target TopicRemovalTarget) (legacycleanup.Candidate, error) {
	var candidates []legacycleanup.Candidate
	add := func(scope, root string, ids, pinned []string, groups []desktopGroup) {
		order := slices.Index(ids, target.TopicID)
		if order < 0 {
			return
		}
		wid := topicRemovalWorkspaceID(state, scope, root)
		if target.WorkspaceID != "" && target.WorkspaceID != wid {
			return
		}
		candidates = append(candidates, legacycleanup.Candidate{Kind: "topic", WorkspaceID: wid, TopicID: target.TopicID,
			Topic: legacyCleanupTopicSnapshot(scope, root, target.TopicID, "", 0, 0, order, pinned, groups)})
	}
	add("global", "", file.GlobalTopics, file.GlobalPinnedTopics, file.GlobalGroups)
	for _, project := range file.Projects {
		add("project", project.Root, project.Topics, project.PinnedTopics, project.Groups)
	}
	if len(candidates) != 1 {
		return legacycleanup.Candidate{}, workspacestate.ErrMutationConflict
	}
	// A topic ID must have one owner even when a caller supplied a workspace.
	owners := 0
	if slices.Contains(file.GlobalTopics, target.TopicID) {
		owners++
	}
	for _, p := range file.Projects {
		if slices.Contains(p.Topics, target.TopicID) {
			owners++
		}
	}
	if owners != 1 {
		return legacycleanup.Candidate{}, workspacestate.ErrMutationConflict
	}
	return candidates[0], nil
}

func topicRemovalToken(item legacycleanup.Candidate, associations any) string {
	body, _ := json.Marshal([]any{item.WorkspaceID, item.TopicID, item.Title, item.Topic, associations})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (a *App) inspectTopicRemovalSnapshot(state workspacestate.State, file desktopProjectFile, snapshot topicstate.Snapshot, item legacycleanup.Candidate) (TopicRemovalInspection, legacycleanup.Candidate, error) {
	out := TopicRemovalInspection{Target: TopicRemovalTarget{WorkspaceID: item.WorkspaceID, TopicID: item.TopicID}}
	record, exists := snapshot.Records[item.TopicID]
	if !exists || record.RowRevision == 0 {
		return out, item, workspacestate.ErrMutationConflict
	}
	item.Title = record.Title
	item.Topic.TitleSource, item.Topic.CreatedAt, item.Topic.RowRevision = record.TitleSource, record.CreatedAtMS, record.RowRevision
	out.Disposition = "archive_placeholder"
	if isDefaultTopicTitle(record.Title) && record.TitleSource == topicTitleSourceAuto && !item.Topic.Pinned && item.Topic.GroupID == "" {
		out.Disposition = "discard_placeholder"
	}
	associations := map[string]any{}
	for id, presentation := range state.Presentation {
		if presentation.TopicID == item.TopicID && state.SessionStates[id].Lifecycle != workspacestate.Deleted {
			out.Disposition = "archive_sessions"
			associations[id] = state.SessionStates[id]
		}
	}
	targets, err := a.topicTrashTargets(item.TopicID)
	if err != nil {
		return out, item, err
	}
	for _, target := range targets {
		associations[target.sessionPath] = target.key
		out.Disposition = "archive_sessions"
	}
	out.Token = topicRemovalToken(item, associations)
	if a.topicHasActiveRuntimeWork(item.TopicID) {
		out.Reason = "busy"
		return out, item, nil
	}
	if out.Disposition != "archive_sessions" {
		if topicRemovalHasPendingCreate(state, item.TopicID) {
			out.Reason = "busy"
			return out, item, nil
		}
		if classification, reason := classifyLegacyCleanupTopicSources(item, a.knownSessionDirs()); classification != "empty" {
			out.Reason = reason
			return out, item, nil
		}
		a.mu.RLock()
		for _, tab := range a.runtimeTabsLocked() {
			if tab != nil && tab.TopicID == item.TopicID && (tab.Ctrl != nil || tab.SessionID != "" || tab.StartupErr == "") {
				out.Reason = "busy"
				break
			}
		}
		a.mu.RUnlock()
		if out.Reason != "" {
			return out, item, nil
		}
	}
	out.Allowed = true
	return out, item, nil
}

func (a *App) InspectTopicRemoval(target TopicRemovalTarget) (TopicRemovalInspection, error) {
	out, _, err := a.inspectTopicRemoval(target)
	return out, sessionOperationErrorForTarget(err, target.TopicID, "")
}

func (a *App) inspectTopicRemoval(target TopicRemovalTarget) (TopicRemovalInspection, legacycleanup.Candidate, error) {
	var out TopicRemovalInspection
	var item legacycleanup.Candidate
	if strings.TrimSpace(target.TopicID) == "" {
		return out, item, workspacestate.ErrMutationConflict
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return out, item, err
	}
	if canonical, found, err := a.inspectCanonicalTopicRemoval(state, target); found || err != nil {
		return canonical, item, err
	}
	file, err := readTopicRemovalProjects()
	if err != nil {
		return out, item, err
	}
	item, err = topicRemovalCandidate(state, file, target)
	if err != nil {
		// Indexed ambiguity must not be bypassed by a source fallback.
		for _, project := range file.Projects {
			if slices.Contains(project.Topics, target.TopicID) {
				return out, item, err
			}
		}
		if slices.Contains(file.GlobalTopics, target.TopicID) {
			return out, item, err
		}
		out, err = a.inspectLegacyTopicRemoval(state, target)
		return out, item, err
	}
	snapshot, err := desktopTopicState.snapshot(topicTitleRoot(item.Topic.Scope, item.Topic.WorkspaceRoot))
	if err != nil {
		return out, item, err
	}
	return a.inspectTopicRemovalSnapshot(state, file, snapshot, item)
}

func (a *App) inspectCanonicalTopicRemoval(state workspacestate.State, target TopicRemovalTarget) (TopicRemovalInspection, bool, error) {
	out := TopicRemovalInspection{Target: target, Disposition: "archive_sessions", Allowed: true}
	token, owner, err := workspacestate.TopicSessionRemovalToken(state, target.WorkspaceID, target.TopicID)
	if err != nil {
		return out, false, err
	}
	if token == "" {
		return out, false, nil
	}
	out.Target.WorkspaceID = owner
	out.Token = token
	if a.topicHasActiveRuntimeWork(target.TopicID) {
		out.Allowed, out.Reason = false, "busy"
	}
	return out, true, nil
}

func (a *App) removeCompatiblePlaceholderAdmissionHeld(topicID string) error {
	inspection, _, err := a.inspectTopicRemoval(TopicRemovalTarget{TopicID: topicID})
	if err != nil {
		return err
	}
	_, err = a.removePlaceholderAdmissionHeld(TopicRemovalRequest{OperationID: "topic-" + newTabID(), Target: inspection.Target, ExpectedToken: inspection.Token})
	return err
}

func (a *App) RemoveTopic(req TopicRemovalRequest) (TopicRemovalResult, error) {
	out := TopicRemovalResult{}
	if req.OperationID == "" || len(req.OperationID) > 200 || req.ExpectedToken == "" || req.Target.WorkspaceID == "" {
		return out, errors.New("invalid topic removal request")
	}
	release, ok := a.tryLockRuntimeMutation("remove topic")
	if !ok {
		out.ErrorCode, out.Retryable = "busy", true
		return out, nil
	}
	defer release()
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err == nil {
		if old, exists := state.TopicRemovals[req.OperationID]; exists && old.WorkspaceID == req.Target.WorkspaceID && old.TopicID == req.Target.TopicID && old.Token == req.ExpectedToken && (old.Phase == "committed" || old.Phase == "restored" || old.Phase == "purged") {
			return topicRemovalResult(old), nil
		}
	}
	if err == nil {
		// Placeholder requests have durable retry state; inspect occurs again in
		// the writer transaction. Session-backed topics retain canonical archive.
		if _, pending := state.TopicRemovals[req.OperationID]; !pending {
			var inspection TopicRemovalInspection
			inspection, _, err = a.inspectTopicRemoval(req.Target)
			if err == nil && (!inspection.Allowed || inspection.Token != req.ExpectedToken) {
				err = workspacestate.ErrMutationConflict
			}
			if err == nil && inspection.Disposition == "archive_sessions" {
				out, err = a.removeTopicSessionsAdmissionHeld(req)
				if err == nil {
					return out, nil
				}
			}
		} else if state.TopicRemovals[req.OperationID].Disposition == "archive_sessions" {
			out, err = a.removeTopicSessionsAdmissionHeld(req)
			if err == nil {
				return out, nil
			}
		}
	}
	if err == nil {
		out, err = a.removePlaceholderAdmissionHeld(req)
	}
	if err != nil {
		out.ErrorCode, out.Retryable = "operation_failed", true
		out.ErrorMessage = err.Error()
		if errors.Is(err, workspacestate.ErrMutationConflict) {
			out.ErrorCode, out.Retryable = "state_conflict", false
		}
		if errors.Is(err, errTopicArchiveBusy) || errors.Is(err, errTopicHasActiveWork) {
			out.ErrorCode = "busy"
		}
		a.emitProjectTreeChanged()
	}
	return out, nil
}

func topicRemovalResult(item workspacestate.TopicRemoval) TopicRemovalResult {
	out := TopicRemovalResult{Committed: true, Disposition: item.Disposition}
	if item.Disposition == "archive_placeholder" {
		out.RecoveryEntryID = "topic-removal:" + item.ID
	}
	return out
}

func (a *App) removeTopicSessionsAdmissionHeld(req TopicRemovalRequest) (TopicRemovalResult, error) {
	// Match AI/manual rename: removal -> title -> index. Cleanup must run
	// after title/index unlock, while removal and runtime admission remain held.
	a.sessionRemovalMu.Lock()
	defer a.sessionRemovalMu.Unlock()
	cleanup := archivedRuntimeCleanup{app: a}
	defer cleanup.finish()
	// Serialize title/organization changes from this process across admission.
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return TopicRemovalResult{}, err
	}
	archiveID := "topic-sessions-" + req.OperationID
	finished := state.PendingOperations[archiveID].Phase == "committed"
	if !finished {
		inspection, _, err := a.inspectTopicRemoval(req.Target)
		if err != nil {
			return TopicRemovalResult{}, err
		}
		if !inspection.Allowed || inspection.Disposition != "archive_sessions" || inspection.Token != req.ExpectedToken {
			return TopicRemovalResult{}, workspacestate.ErrMutationConflict
		}
	}
	err = a.workspaceRegistry().TransitionTopicRemoval(a.bootContext(), req.OperationID, func(state workspacestate.State, item *workspacestate.TopicRemoval, _ func() error) error {
		if item.ID != "" {
			if item.WorkspaceID != req.Target.WorkspaceID || item.TopicID != req.Target.TopicID || item.Token != req.ExpectedToken || item.Disposition != "archive_sessions" {
				return workspacestate.ErrMutationConflict
			}
			return nil
		}
		token, _, err := workspacestate.TopicSessionRemovalToken(state, req.Target.WorkspaceID, req.Target.TopicID)
		if err != nil {
			return err
		}
		if token != "" && token != req.ExpectedToken {
			return workspacestate.ErrMutationConflict
		}
		*item = workspacestate.TopicRemoval{ID: req.OperationID, WorkspaceID: req.Target.WorkspaceID, TopicID: req.Target.TopicID, Token: req.ExpectedToken, SessionToken: token, Disposition: "archive_sessions", Phase: "prepared"}
		return nil
	})
	if err != nil {
		return TopicRemovalResult{}, err
	}
	if !finished {
		a.lifecycleCheckpoint("topic-sessions-before-archive")
		if err := a.archiveCompatibleTopicWithCleanupAdmissionHeld(req.Target.TopicID, archiveID, &cleanup); err != nil {
			return TopicRemovalResult{}, err
		}
	}
	err = a.workspaceRegistry().TransitionTopicRemoval(a.bootContext(), req.OperationID, func(_ workspacestate.State, item *workspacestate.TopicRemoval, _ func() error) error {
		item.Phase = "committed"
		return nil
	})
	return TopicRemovalResult{Committed: err == nil, Disposition: "archive_sessions"}, err
}

func (a *App) removePlaceholderAdmissionHeld(req TopicRemovalRequest) (TopicRemovalResult, error) {
	var result workspacestate.TopicRemoval
	if !a.sessionRemovalMu.TryLock() {
		return TopicRemovalResult{}, errTopicArchiveBusy
	}
	defer a.sessionRemovalMu.Unlock()
	var removed []removedSessionRuntime
	defer func() { a.finalizeRemovedTopicRuntimes(removed) }()
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	err := a.workspaceRegistry().TransitionTopicRemoval(a.bootContext(), req.OperationID, func(state workspacestate.State, operation *workspacestate.TopicRemoval, checkpoint func() error) error {
		if operation.ID != "" {
			if operation.Token != req.ExpectedToken || operation.TopicID != req.Target.TopicID || operation.WorkspaceID != req.Target.WorkspaceID {
				return workspacestate.ErrMutationConflict
			}
			if operation.Phase == "committed" || operation.Phase == "restored" || operation.Phase == "purged" {
				result = *operation
				return nil
			}
			if operation.Phase != "prepared" {
				return workspacestate.ErrMutationConflict
			}
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
		var item legacycleanup.Candidate
		if operation.ID != "" {
			if err := json.Unmarshal(operation.Snapshot, &item); err != nil {
				return err
			}
		} else {
			item, err = topicRemovalCandidate(state, file, req.Target)
			if err != nil {
				return err
			}
		}
		if item.Topic == nil {
			return workspacestate.ErrMutationConflict
		}
		if topicRemovalHasOtherOwner(file, item) {
			return workspacestate.ErrMutationConflict
		}
		err = desktopTopicState.withExclusiveScope(topicTitleRoot(item.Topic.Scope, item.Topic.WorkspaceRoot), func(ctx context.Context, store *topicstate.Store) error {
			return a.removePlaceholderMetadataLocked(ctx, store, state, file, item, req, operation, checkpoint)
		})
		result = *operation
		return err
	})
	if err != nil {
		return TopicRemovalResult{}, err
	}
	captured := a.captureTopicRuntimeBindings(req.Target.TopicID)
	_, unchanged := a.removeTopicRuntimeBindingsIfUnchanged(req.Target.TopicID, captured)
	if unchanged {
		removed = captured
	}
	a.emitProjectTreeChanged()
	return topicRemovalResult(result), nil
}

func (a *App) checkRemovedTopicSources(state workspacestate.State, item legacycleanup.Candidate) error {
	if topicRemovalHasPendingCreate(state, item.TopicID) {
		return errTopicHasActiveWork
	}
	for id, presentation := range state.Presentation {
		if presentation.TopicID == item.TopicID && state.SessionStates[id].Lifecycle != workspacestate.Deleted {
			return workspacestate.ErrMutationConflict
		}
	}
	if classification, _ := classifyLegacyCleanupTopicSources(item, a.knownSessionDirs()); classification != "empty" {
		return workspacestate.ErrMutationConflict
	}
	if a.topicHasActiveRuntimeWork(item.TopicID) {
		return errTopicHasActiveWork
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.TopicID == item.TopicID && (tab.Ctrl != nil || tab.SessionID != "" || tab.StartupErr == "") {
			return errTopicHasActiveWork
		}
	}
	return nil
}

func topicRemovalHasPendingCreate(state workspacestate.State, topicID string) bool {
	for _, create := range state.PendingCreates {
		if create.Presentation != nil && create.Presentation.TopicID == topicID {
			return true
		}
	}
	for _, operation := range state.PendingOperations {
		if operation.Phase != "committed" && operation.Presentation != nil && operation.Presentation.TopicID == topicID {
			return true
		}
	}
	return false
}

func topicRemovalError(result TopicRemovalResult) error {
	if result.Committed {
		return nil
	}
	return fmt.Errorf("topic removal incomplete: %s", result.ErrorCode)
}

// Requires registry, projects and topic-store writer ownership.
func (a *App) removePlaceholderMetadataLocked(ctx context.Context, store *topicstate.Store, state workspacestate.State, file desktopProjectFile, item legacycleanup.Candidate, req TopicRemovalRequest, operation *workspacestate.TopicRemoval, checkpoint func() error) error {
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return err
	}
	indexed := topicIndexedInProjectsSnapshot(file, item.Topic.Scope, item.Topic.WorkspaceRoot, item.TopicID)
	if indexed {
		current, err := topicRemovalCandidate(state, file, req.Target)
		if err != nil {
			return err
		}
		inspection, captured, err := a.inspectTopicRemovalSnapshot(state, file, snapshot, current)
		if err != nil {
			return err
		}
		if !inspection.Allowed || inspection.Token != req.ExpectedToken || inspection.Disposition == "archive_sessions" {
			return workspacestate.ErrMutationConflict
		}
		item = captured
		if operation.ID == "" {
			body, err := json.Marshal(item)
			if err != nil {
				return err
			}
			*operation = workspacestate.TopicRemoval{ID: req.OperationID, WorkspaceID: item.WorkspaceID, TopicID: item.TopicID, Token: req.ExpectedToken, Disposition: inspection.Disposition, Phase: "prepared", ArchivedAt: time.Now().UnixMilli(), Snapshot: body}
			operation.Metadata, err = json.Marshal(snapshot.Records[item.TopicID])
			if err != nil {
				return err
			}
			if err := checkpoint(); err != nil {
				return err
			}
		}
	} else {
		if operation.ID == "" || !slices.Contains(file.DeletedTopics, item.TopicID) {
			return workspacestate.ErrMutationConflict
		}
		if r, exists := snapshot.Records[item.TopicID]; exists && r.RowRevision != item.Topic.RowRevision {
			return workspacestate.ErrMutationConflict
		}
		if err := a.checkRemovedTopicSources(state, item); err != nil {
			return err
		}
	}
	a.lifecycleCheckpoint("topic-removal-before-index")
	if err := removeTopicFromProjectsFileCrossProcessLocked(item.TopicID); err != nil {
		return err
	}
	a.lifecycleCheckpoint("topic-removal-before-metadata")
	if _, err := store.Delete(ctx, item.TopicID); err != nil {
		return err
	}
	a.lifecycleCheckpoint("topic-removal-before-commit")
	operation.Phase = "committed"
	if operation.Disposition == "discard_placeholder" {
		operation.Snapshot = nil
		operation.Metadata = nil
	}
	return nil
}
