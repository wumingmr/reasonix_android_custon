package workspacestate

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
)

// Source identities written before physical-path normalization remain durable
// receipts. Add read aliases without rewriting their keys or operation journals.
type sourceIdentityIndex struct {
	aliases map[string][]string
	owners  map[string][]string
}

func newSourceIdentityIndex(state State) *sourceIdentityIndex {
	index := &sourceIdentityIndex{aliases: map[string][]string{}, owners: map[string][]string{}}
	paths := map[string]string{}
	add := func(mapping SourceMapping, committed bool) {
		key := mapping.SourceKey
		if key == "" {
			return
		}
		keys, known := index.aliases[key]
		if !known {
			keys = []string{key}
			path, resolved := paths[mapping.Path]
			if !resolved {
				path, _ = sourcePathKey(mapping.Path)
				paths[mapping.Path] = path
			}
			if path != "" {
				heads := []string{mapping.HeadID}
				// Old lineage receipts kept the originating head for a single-session
				// directory; discovery uses no head. Legacy DAG heads stay distinct.
				if mapping.Format == "canonical" && mapping.HeadID != "" {
					heads = append(heads, "")
				}
				for _, head := range heads {
					normalized := fmt.Sprintf("%x", sha256.Sum256([]byte(path+"\x00"+head)))
					if _, version, found := strings.Cut(key, ":review:"); found {
						normalized += ":review:" + version
					}
					if normalized != key {
						keys = append(keys, normalized)
					}
				}
			}
			index.aliases[key] = keys
		}
		if committed {
			for _, alias := range keys {
				index.owners[alias] = append(index.owners[alias], key)
			}
		}
	}
	for _, mapping := range state.SourceMappings {
		add(mapping, true)
	}
	for _, op := range state.PendingOperations {
		if op.Mapping != nil {
			add(*op.Mapping, false)
		}
	}
	return index
}

func (s State) sourceIdentityIndex() *sourceIdentityIndex {
	if s.sourceIdentities != nil {
		return s.sourceIdentities
	}
	return newSourceIdentityIndex(s)
}

// SourceKeys returns durable storage and current lookup identities. Legacy DAG
// heads and reviewed versions remain independent from their parent source.
func (s State) SourceKeys(key string) []string {
	keys := s.sourceIdentityIndex().aliases[key]
	if len(keys) == 0 {
		return []string{key}
	}
	return slices.Clone(keys)
}

// ResolveSource rejects ambiguous normalized ownership instead of choosing a
// destination or admitting another import. Exact durable keys remain usable.
func (s State) ResolveSource(key string) (SourceMapping, bool, error) {
	if mapping, ok := s.SourceMappings[key]; ok {
		return cloneSourceMapping(mapping), true, nil
	}
	var result SourceMapping
	found := false
	for _, owner := range s.sourceIdentityIndex().owners[key] {
		mapping, exists := s.SourceMappings[owner]
		if !exists {
			continue
		}
		if found && (result.SessionID != mapping.SessionID || result.WorkspaceID != mapping.WorkspaceID || result.Fingerprint != mapping.Fingerprint) {
			return SourceMapping{}, false, ErrMutationConflict
		}
		if !found || mapping.SourceKey < result.SourceKey {
			result = mapping
		}
		found = true
	}
	return cloneSourceMapping(result), found, nil
}
