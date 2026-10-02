package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

// Explicit source archives stage content as archived from the beginning. They
// must not use PrepareSession, which publishes an active/openable conversation.
func (a *App) archiveHistoricalSource(selector SessionSelector) (SessionMutationResult, error) {
	if selector.Source != nil && selector.Source.HostID != "" && selector.Source.HostID != localDesktopHostID {
		return SessionMutationResult{}, newSessionOperationError("unsupported", "This source belongs to another host.")
	}
	runtimeRelease, ok := a.tryLockRuntimeMutation("archive historical source")
	if !ok {
		return SessionMutationResult{}, sessionOperationErrorForTarget(errTopicArchiveBusy, "", "")
	}
	defer runtimeRelease()
	ctx, done, err := a.beginHistoricalRecovery()
	if err != nil {
		return SessionMutationResult{}, err
	}
	defer done()
	id, source, err := a.historicalSourceForSelector(selector)
	if err != nil {
		return SessionMutationResult{}, err
	}
	operationID := "archive-source-" + strings.TrimPrefix(newTabID(), "tab_")
	result, err := a.archiveHistoricalSourceWithOperation(ctx, id, source, operationID)
	if err != nil {
		slog.Warn("desktop: historical archive failed", "source_key", id, "operation", operationID, "err", err)
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, id, operationID)
	}
	archivedPaths := []string{source.path}
	siblings := legacyHeadVersions(source)
	for _, path := range recoveredLegacySiblings(source) {
		siblings = append(siblings, SessionSourceRef{HostID: localDesktopHostID, Path: path})
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		slog.Warn("desktop: sibling lifecycle unavailable", "source_key", id, "err", err)
	}
	for _, sibling := range siblings {
		siblingID, siblingSource, err := a.historicalSourceForSelector(SessionSelector{Source: &sibling})
		if err != nil {
			slog.Warn("desktop: sibling archive skipped", "source_key", id, "err", err)
			continue
		}
		if historicalSourceMapped(state, siblingID) {
			continue
		}
		siblingResult, err := a.archiveHistoricalSourceWithOperation(ctx, siblingID, siblingSource,
			"archive-source-"+strings.TrimPrefix(newTabID(), "tab_"))
		if err != nil {
			slog.Warn("desktop: sibling archive failed", "source_key", siblingID, "err", err)
			continue
		}
		result.IdentityAliases = append(result.IdentityAliases, siblingResult.IdentityAliases...)
		archivedPaths = append(archivedPaths, siblingSource.path)
	}
	c := &a.historicalImports
	c.mu.Lock()
	for i := range c.catalog {
		for _, path := range archivedPaths {
			if c.catalog[i].node.Source != nil && sameDesktopPath(c.catalog[i].node.Source.Path, path) {
				c.catalog[i].sourceChanged = false
			}
		}
	}
	c.mu.Unlock()
	a.emitProjectTreeChanged()
	return result, nil
}

// recoveredLegacySiblings lists the other recovery copies of the conversation a
// recovered legacy source was copied from: same directory, recovered, and the
// same parent in the metadata their writer recorded. The sidebar lists each
// copy as its own row of the same conversation, so archiving one row alone
// only brings the next copy up in its place.
func recoveredLegacySiblings(source historicalSource) []string {
	if source.format != "legacy" {
		return nil
	}
	meta, ok, err := agent.LoadBranchMeta(source.path)
	if err != nil || !ok || !meta.Recovered || strings.TrimSpace(meta.ParentID) == "" {
		return nil
	}
	dir := filepath.Dir(source.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var siblings []string
	for _, entry := range entries {
		if entry.IsDir() || !store.IsSessionTranscriptName(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if sameDesktopPath(path, source.path) {
			continue
		}
		other, ok, err := agent.LoadBranchMeta(path)
		if err == nil && ok && other.Recovered && other.ParentID == meta.ParentID {
			siblings = append(siblings, path)
		}
	}
	return siblings
}

// A sibling with a durable mapping already has its own lifecycle. Archiving a
// different version must not retire an active session or copy an old version
// again after another head changes the shared legacy transcript.
func historicalSourceMapped(state workspacestate.State, id string) bool {
	_, mapped, err := state.ResolveSource(id)
	return err == nil && mapped
}

// headStartsConversation reports a head the user split off under a name. A
// rewind, an unnamed fork ("fork from this turn") and a second writer's
// concurrent head all continue their parent head's conversation.
func headStartsConversation(head agent.SessionHead) bool {
	return head.ParentHead == "" || head.Kind == agent.HeadKindFork && strings.TrimSpace(head.Name) != ""
}

// legacyHeadVersions lists the other live heads of a legacy transcript that are
// versions of the selected head's conversation. The sidebar lists every version
// as its own row with the same title; named forks stay.
func legacyHeadVersions(source historicalSource) []SessionSourceRef {
	if source.format != "legacy" || source.head == "" {
		return nil
	}
	heads, err := agent.ListSessionHeads(source.path)
	if err != nil {
		return nil
	}
	byID := make(map[string]agent.SessionHead, len(heads))
	for _, head := range heads {
		byID[head.ID] = head
	}
	origin := func(id string) string {
		for range heads {
			head, ok := byID[id]
			if !ok || headStartsConversation(head) {
				break
			}
			id = head.ParentHead
		}
		return id
	}
	if _, ok := byID[source.head]; !ok {
		return nil
	}
	want := origin(source.head)
	var versions []SessionSourceRef
	for _, head := range heads {
		if head.ID == source.head || head.Retired || head.Kind == agent.HeadKindConcurrent || origin(head.ID) != want {
			continue
		}
		versions = append(versions, SessionSourceRef{HostID: localDesktopHostID, Path: source.path, HeadID: head.ID})
	}
	return versions
}

func (a *App) archiveHistoricalSourceWithOperation(ctx context.Context, id string, source historicalSource, operationID string) (SessionMutationResult, error) {
	release, err := acquireHistoricalSource(ctx, id, source)
	if err != nil {
		return SessionMutationResult{}, err
	}
	defer release()
	fingerprint, err := desktopSourceFingerprint(source.path)
	if err != nil {
		return SessionMutationResult{}, err
	}
	mapping, dependencies, err := a.stageHistoricalArchive(ctx, id, source, fingerprint)
	if err != nil {
		return SessionMutationResult{}, err
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionMutationResult{}, err
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	if len(dependencies) != 0 {
		operationID = "archive-" + dependencies[0]
	}
	lifecycle := state.SessionStates[ref.SessionID]
	outcome := "archived"
	if lifecycle.Lifecycle == workspacestate.Deleted {
		outcome = "already_removed"
		if err := retireRemovedSourceTopic(state, source); err != nil {
			return SessionMutationResult{}, fmt.Errorf("retire removed source topic: %w", err)
		}
	} else if lifecycle.Lifecycle == workspacestate.Archived {
		if _, err := a.desktopSessionService("").Query().Snapshot(ctx, ref); err != nil {
			return SessionMutationResult{}, err
		}
	} else if lifecycle.Lifecycle != workspacestate.Archived {
		verify := func(ctx context.Context, current workspacestate.State) error {
			fp, err := desktopSourceFingerprint(source.path)
			if err != nil || fp != fingerprint {
				return errors.Join(err, workspacestate.ErrMutationConflict)
			}
			for _, dependency := range dependencies {
				if err := a.validateHistoricalArchiveContent(ctx, source, current.PendingOperations[dependency]); err != nil {
					return err
				}
			}
			return nil
		}
		if err := a.archiveSessionRefsWithOperationConditional([]session.SessionRef{ref}, operationID, verify, dependencies...); err != nil {
			return SessionMutationResult{}, fmt.Errorf("commit historical archive: %w", err)
		}
		state, err = a.workspaceRegistry().Load(ctx)
		if err != nil {
			return SessionMutationResult{}, err
		}
		lifecycle = state.SessionStates[ref.SessionID]
	}
	if outcome != "already_removed" && source.format == "canonical" && ref.SessionID != filepath.Base(source.path) {
		outcome = "archived_copy"
	}
	projectionPending := false
	if outcome != "already_removed" {
		if err := a.applyHistoricalSourcePresentation(desktopSourceKey(source.path, source.head), ref); err != nil {
			// Archive is durable, just as import can succeed with presentation
			// pending. Retrying the source action reapplies its saved overlay.
			projectionPending = true
			slog.Warn("desktop: archived source presentation pending", "source_key", id, "operation", operationID, "err", err)
		}
	}
	target := SessionTarget{SessionRef: ref, Scope: source.scope, WorkspaceRoot: source.root}
	aliases := a.sessionTargetIdentityAliases(target)
	aliases = append(aliases, "source\x00local\x00"+id)
	return SessionMutationResult{TargetKey: target.key(), OperationID: operationID, Committed: true,
		Outcome: outcome, LifecycleGeneration: lifecycle.Generation, IdentityAliases: aliases, ProjectionPending: projectionPending}, nil
}
