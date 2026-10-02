package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/session"
)

type legacyCleanupBatchBuilder struct {
	app           *App
	state         workspacestate.State
	items         map[string]legacycleanup.Candidate
	mappedSources map[string]bool
	seenLegacy    map[string]bool
}

func newLegacyCleanupBatchBuilder(app *App, state workspacestate.State) *legacyCleanupBatchBuilder {
	mapped := make(map[string]bool, len(state.SourceMappings))
	for _, mapping := range state.SourceMappings {
		mapped[sessionRuntimeKey(mapping.Path)+"\x00"+mapping.HeadID] = true
	}
	return &legacyCleanupBatchBuilder{
		app: app, state: state, items: map[string]legacycleanup.Candidate{},
		mappedSources: mapped, seenLegacy: map[string]bool{},
	}
}

func (b *legacyCleanupBatchBuilder) registerCanonicalSessions(ctx context.Context) {
	query := b.app.desktopSessionService("").Query()
	for workspaceID, workspace := range b.state.Workspaces {
		for _, sessionID := range workspace.SessionIDs {
			status := b.state.SessionStates[sessionID]
			if status.Lifecycle != "" && status.Lifecycle != workspacestate.Active {
				continue
			}
			info, statErr := query.Stat(ctx, session.SessionRef{HostID: localDesktopHostID, SessionID: sessionID})
			title := b.state.Presentation[sessionID].Title
			if statErr == nil && (info.TitleSequence > 0 || strings.TrimSpace(info.Title) != "") {
				title = info.Title
			}
			if statErr == nil && !isDefaultTopicTitle(title) {
				continue
			}
			candidate := legacycleanup.Candidate{
				ID: "session:" + sessionID, Kind: "session", WorkspaceID: workspaceID, SessionID: sessionID,
				Title: title, TitleSequence: info.TitleSequence, EventSequence: info.EventSequence,
				LifecycleGeneration: status.Generation, OperationID: "legacy-empty-" + sessionID, Phase: "registered",
			}
			candidate.Sources = b.sourcesForSession(sessionID)
			b.items[candidate.ID] = candidate
		}
	}
}

func (b *legacyCleanupBatchBuilder) sourcesForSession(sessionID string) []legacycleanup.SourceSnapshot {
	var sources []legacycleanup.SourceSnapshot
	for _, mapping := range b.state.SourceMappings {
		if mapping.SessionID != sessionID || mapping.Format != "legacy" {
			continue
		}
		fingerprint, _ := legacyCleanupSourceFingerprint(mapping.Path)
		sources = append(sources, legacycleanup.SourceSnapshot{Path: mapping.Path, HeadID: mapping.HeadID, Fingerprint: fingerprint})
	}
	slices.SortFunc(sources, func(left, right legacycleanup.SourceSnapshot) int {
		return strings.Compare(sessionRuntimeKey(left.Path)+"\x00"+left.HeadID, sessionRuntimeKey(right.Path)+"\x00"+right.HeadID)
	})
	return sources
}

func (b *legacyCleanupBatchBuilder) registerLegacy(scope, root, workspaceID, topicID, title string) bool {
	registered := false
	for _, dir := range b.app.knownSessionDirs() {
		for _, match := range topicSessionMatches(dir, topicID) {
			if !topicSessionMatchMatchesTarget(match, scope, root) {
				continue
			}
			path := filepath.Clean(match.path)
			heads, err := session.LegacyMigrationHeads(b.app.bootContext(), path)
			if err != nil || len(heads) == 0 {
				heads = []agent.SessionHead{{}}
			}
			fingerprint, _ := legacyCleanupSourceFingerprint(path)
			for _, head := range heads {
				if head.Retired || !b.registerLegacyHead(workspaceID, topicID, title, path, fingerprint, head.ID) {
					continue
				}
				registered = true
			}
		}
	}
	return registered
}

func (b *legacyCleanupBatchBuilder) registerLegacyHead(workspaceID, topicID, title, path, fingerprint, headID string) bool {
	identity := sessionRuntimeKey(path) + "\x00" + headID
	if b.seenLegacy[identity] || b.mappedSources[identity] {
		return false
	}
	b.seenLegacy[identity] = true
	key := desktopSourceKey(path, headID)
	id := "legacy:" + key
	b.items[id] = legacycleanup.Candidate{
		ID: id, Kind: "legacy", WorkspaceID: workspaceID, TopicID: topicID,
		SourcePath: path, SourceHeadID: headID, SourceFingerprint: fingerprint,
		Title: title, OperationID: "legacy-empty-source-" + key, Phase: "registered",
	}
	return true
}

func (b *legacyCleanupBatchBuilder) registerTopics(scope, root, workspaceID string, ids, pinned []string, groups []desktopGroup) {
	topicRoot := topicTitleRoot(scope, root)
	titles, sources, created, rowRevisions := legacyCleanupTopicMetadata(topicRoot)
	for order, topicID := range ids {
		title := titles[topicID]
		if !isDefaultTopicTitle(title) {
			continue
		}
		legacyRegistered := b.registerLegacy(scope, root, workspaceID, topicID, title)
		if b.topicHasCanonicalSession(topicID) || legacyRegistered {
			continue
		}
		snapshot := legacyCleanupTopicSnapshot(scope, root, topicID, sources[topicID], created[topicID], rowRevisions[topicID], order, pinned, groups)
		id := "topic:" + topicID
		b.items[id] = legacycleanup.Candidate{ID: id, Kind: "topic", WorkspaceID: workspaceID, TopicID: topicID, Title: title, OperationID: "legacy-empty-topic-" + topicID, Phase: "registered", Topic: snapshot}
	}
}

func legacyCleanupTopicMetadata(root string) (map[string]string, map[string]string, map[string]int64, map[string]int64) {
	titles, sources := map[string]string{}, map[string]string{}
	created, revisions := map[string]int64{}, map[string]int64{}
	snapshot, err := desktopTopicState.snapshot(root)
	if err != nil {
		return loadTopicTitles(root), loadTopicTitleSources(root), loadTopicCreatedAts(root), revisions
	}
	for topicID, record := range snapshot.Records {
		titles[topicID] = agent.UserPreviewText(record.Title)
		sources[topicID] = record.TitleSource
		created[topicID] = record.CreatedAtMS
		revisions[topicID] = record.RowRevision
	}
	return titles, sources, created, revisions
}

func (b *legacyCleanupBatchBuilder) topicHasCanonicalSession(topicID string) bool {
	for _, presentation := range b.state.Presentation {
		if presentation.TopicID == topicID {
			return true
		}
	}
	return false
}

func (b *legacyCleanupBatchBuilder) workspaceIDForRoot(root string) string {
	for id, workspace := range b.state.Workspaces {
		if sameDesktopPath(workspace.Root, root) {
			return id
		}
	}
	return ""
}

func legacyCleanupTopicSnapshot(scope, root, topicID, source string, createdAt, rowRevision int64, order int, pinned []string, groups []desktopGroup) *legacycleanup.TopicSnapshot {
	snapshot := &legacycleanup.TopicSnapshot{
		Scope: scope, WorkspaceRoot: root, TitleSource: source, CreatedAt: createdAt,
		RowRevision: rowRevision, Order: order, Pinned: slices.Contains(pinned, topicID), GroupOrder: -1,
	}
	for _, group := range groups {
		if index := slices.Index(group.TopicIDs, topicID); index >= 0 {
			snapshot.GroupID, snapshot.GroupOrder = group.ID, index
			break
		}
	}
	return snapshot
}
