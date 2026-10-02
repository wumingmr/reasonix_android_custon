package workspacestate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ReadVersions contains only immutable invalidation tokens. It does not grant
// execution authority or retain a second mutable copy of registry records.
// Construct once per verified publication, then query only visible identities.
type ReadVersions struct {
	sources  map[string]string
	sessions map[string]string
}

type sourceReadBinding struct {
	Mapping SourceMapping
	State   SessionState
}

func readVersion(value any) string {
	body, _ := json.Marshal(value)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// NewReadVersions reads the supplied snapshot without retaining or changing its
// mutable fields. Source aliases are resolved once per distinct saved path.
func NewReadVersions(state State) *ReadVersions {
	v := &ReadVersions{sources: map[string]string{}, sessions: map[string]string{}}
	byPath := map[string]map[string]sourceReadBinding{}
	bySession := map[string]map[string]SourceMapping{}
	pathKeys := map[string]string{}
	for key, mapping := range state.SourceMappings {
		pathKey, known := pathKeys[mapping.Path]
		if !known {
			pathKey, _ = sourcePathKey(mapping.Path)
			pathKeys[mapping.Path] = pathKey
		}
		if pathKey != "" {
			if byPath[pathKey] == nil {
				byPath[pathKey] = map[string]sourceReadBinding{}
			}
			byPath[pathKey][key] = sourceReadBinding{mapping, state.SessionStates[mapping.SessionID]}
		}
		if bySession[mapping.SessionID] == nil {
			bySession[mapping.SessionID] = map[string]SourceMapping{}
		}
		bySession[mapping.SessionID][key] = mapping
	}
	for path, bindings := range byPath {
		v.sources[path] = readVersion(bindings)
	}
	for id, status := range state.SessionStates {
		v.sessions[id] = readVersion([]any{status, bySession[id]})
	}
	// Preserve mapping-only identities as well. Older registries can omit the
	// explicit lifecycle entry until their first owned mutation.
	for id, mappings := range bySession {
		if _, exists := v.sessions[id]; !exists {
			v.sessions[id] = readVersion([]any{SessionState{}, mappings})
		}
	}
	return v
}

func (v *ReadVersions) Source(path string) (string, error) {
	if len(v.sources) == 0 {
		return "", nil
	}
	key, err := sourcePathKey(path)
	if err != nil {
		return "", err
	}
	return v.sources[key], nil
}

func (v *ReadVersions) Session(id string) string { return v.sessions[id] }

func (r *ReadSnapshot) ReadVersions() *ReadVersions { return r.versions }
