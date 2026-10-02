package workspacestate

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// PurgeSourceCleanup records the exact adopted sources eligible for an explicit
// deletion request. Older purge journals without this request retain originals.
// Multi-file legacy DAGs are retained: their independent heads and artifacts
// cannot be deleted as one canonical session directory.
type PurgeSourceCleanup struct {
	Version int             `json:"sourceCleanupVersion"`
	Sources []SourceMapping `json:"sources"`
}

func (s *Store) BeginPurgeWithSources(ctx context.Context, id string, expected uint64) error {
	return s.beginOrResumePurge(ctx, id, expected, nil, true)
}

func purgeSourceCleanupRequest(state State, id string) (json.RawMessage, error) {
	plan := PurgeSourceCleanup{Version: 1}
	for _, mapping := range state.SourceMappings {
		if mapping.SessionID == id && mapping.Format == "canonical" && mapping.HeadID == "" {
			mapping.RetainedArtifacts = nil
			plan.Sources = append(plan.Sources, mapping)
		}
	}
	slices.SortFunc(plan.Sources, func(a, b SourceMapping) int { return strings.Compare(a.SourceKey, b.SourceKey) })
	return json.Marshal(plan)
}
