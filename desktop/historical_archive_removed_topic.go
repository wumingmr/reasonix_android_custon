package main

import (
	"os"
	"path/filepath"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/store"
)

// retireRemovedSourceTopic makes clearing a source whose session is already
// deleted durable: its legacy topic joins deletedTopics, which every sidebar
// projection honours. A topic that anything else still carries stays listed.
func retireRemovedSourceTopic(state workspacestate.State, source historicalSource) error {
	if source.format == "canonical" {
		return nil
	}
	meta, ok, err := agent.LoadBranchMeta(source.path)
	if err != nil || !ok {
		return err
	}
	topicID := strings.TrimSpace(meta.TopicID)
	if topicID == "" || topicHasSurvivingOwner(state, topicID) {
		return nil
	}
	shared, err := legacyTopicSharedBeyond(state, source.path, topicID)
	if err != nil || shared {
		return err
	}
	return removeTopicFromProjectsFile(topicID)
}

func topicHasSurvivingOwner(state workspacestate.State, topicID string) bool {
	for id, presentation := range state.Presentation {
		if presentation.TopicID == topicID && state.SessionStates[id].Lifecycle != workspacestate.Deleted {
			return true
		}
	}
	for _, pending := range state.PendingCreates {
		if pending.Presentation != nil && pending.Presentation.TopicID == topicID {
			return true
		}
	}
	return false
}

// legacyTopicSharedBeyond reports whether another transcript beside path
// carries topicID without being adopted by a deleted session. Unreadable
// metadata cannot prove the topic is unshared.
func legacyTopicSharedBeyond(state workspacestate.State, path, topicID string) (bool, error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		sibling := filepath.Join(filepath.Dir(path), entry.Name())
		if entry.IsDir() || !store.IsSessionTranscriptName(entry.Name()) || sameDesktopPath(sibling, path) {
			continue
		}
		meta, ok, err := agent.LoadBranchMeta(sibling)
		if err != nil || !ok {
			return true, nil
		}
		if strings.TrimSpace(meta.TopicID) == topicID && !sourceAdoptedByDeletedSession(state, sibling) {
			return true, nil
		}
	}
	return false, nil
}

func sourceAdoptedByDeletedSession(state workspacestate.State, path string) bool {
	for _, mapping := range state.SourceMappings {
		if sameDesktopPath(mapping.Path, path) && state.SessionStates[mapping.SessionID].Lifecycle == workspacestate.Deleted {
			return true
		}
	}
	return false
}
