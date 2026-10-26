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
// Receipts of one source and content split across sessions settle on the single
// live owner; the retired copies keep their receipts and content untouched.
func (s State) ResolveSource(key string) (SourceMapping, bool, error) {
	if mapping, ok := s.SourceMappings[key]; ok {
		return cloneSourceMapping(mapping), true, nil
	}
	var owners []SourceMapping
	for _, owner := range s.sourceIdentityIndex().owners[key] {
		if mapping, exists := s.SourceMappings[owner]; exists {
			owners = append(owners, mapping)
		}
	}
	if len(owners) == 0 {
		return SourceMapping{}, false, nil
	}
	slices.SortFunc(owners, func(a, b SourceMapping) int { return strings.Compare(a.SourceKey, b.SourceKey) })
	if !sameOwner(owners[0], owners[1:]) {
		live, ok := s.liveDuplicateOwners(owners)
		if !ok {
			return SourceMapping{}, false, ErrMutationConflict
		}
		owners = live
	}
	return cloneSourceMapping(owners[0]), true, nil
}

func sameOwner(first SourceMapping, rest []SourceMapping) bool {
	for _, other := range rest {
		if other.SessionID != first.SessionID || other.WorkspaceID != first.WorkspaceID || other.Fingerprint != first.Fingerprint {
			return false
		}
	}
	return true
}

// liveDuplicateOwners narrows receipts that disagree only on which session
// holds them to the mappings of the one session still in use. Different
// content, workspaces, or zero or several live sessions stay ambiguous.
func (s State) liveDuplicateOwners(owners []SourceMapping) ([]SourceMapping, bool) {
	first := owners[0]
	var live []SourceMapping
	for _, owner := range owners {
		if owner.Fingerprint == "" || owner.Fingerprint != first.Fingerprint || owner.WorkspaceID != first.WorkspaceID {
			return nil, false
		}
		if lifecycle := s.SessionStates[owner.SessionID].Lifecycle; lifecycle == Active {
			live = append(live, owner)
		}
	}
	if len(live) == 0 || !sameOwner(live[0], live[1:]) {
		return nil, false
	}
	return live, true
}
