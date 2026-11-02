package main

import (
	"sort"
	"strings"

	"reasonix/desktop/internal/workspacestate"
)

// historicalVersionSourceKey reports whether key names a reviewed version: the
// original source key, ":review:", then that version's fingerprint.
func historicalVersionSourceKey(key string) bool {
	source, version, reviewed := strings.Cut(key, ":review:")
	return reviewed && source != "" && version != ""
}

func historicalSourceKeyMatches(mappingKey, sourceID string) bool {
	return mappingKey == sourceID || strings.HasPrefix(mappingKey, sourceID+":review:")
}

func historicalMappingForSource(state workspacestate.State, sourceID string) (workspacestate.SourceMapping, bool, error) {
	if mapping, ok, err := state.ResolveSource(sourceID); ok || err != nil {
		return mapping, ok, err
	}
	keys := make([]string, 0, len(state.SourceMappings))
	for key := range state.SourceMappings {
		for _, alias := range state.SourceKeys(key) {
			if historicalSourceKeyMatches(alias, sourceID) {
				keys = append(keys, key)
				break
			}
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return workspacestate.SourceMapping{}, false, nil
	}
	return state.SourceMappings[keys[0]], true, nil
}
