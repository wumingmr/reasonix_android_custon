package workspacestate

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestReadVersionsTrackSourceAndLifecycleWithoutPresentation(t *testing.T) {
	state := newState()
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	state.SessionStates["one"] = SessionState{Lifecycle: Active, Generation: 3}
	state.Presentation["one"] = Presentation{Title: "before"}
	state.SourceMappings["adopted"] = SourceMapping{Path: path, HeadID: "branch", SessionID: "one", Fingerprint: "proof", RetainedArtifacts: []string{"checkpoint"}}
	first := NewReadVersions(state)
	source, err := first.Source(path)
	if err != nil || source == "" || first.Session("one") == "" {
		t.Fatalf("missing version: %q %v", source, err)
	}
	state.Presentation["one"] = Presentation{Title: "renamed", Pinned: true}
	state.SessionStates["unrelated"] = SessionState{Lifecycle: Archived, Generation: 19}
	unchanged := NewReadVersions(state)
	if got, _ := unchanged.Source(path); got != source || unchanged.Session("one") != first.Session("one") {
		t.Fatal("presentation or an unrelated session revoked a snapshot")
	}
	// Mutating an input slice must not change an already published token.
	state.SourceMappings["adopted"].RetainedArtifacts[0] = "replaced"
	replacement := NewReadVersions(state)
	if got, _ := first.Source(path); got != source {
		t.Fatal("input mutation changed retained versions")
	}
	if got, _ := replacement.Source(path); got == source || replacement.Session("one") == first.Session("one") {
		t.Fatal("source mapping replacement did not revoke the old version")
	}
	state.SessionStates["one"] = SessionState{Lifecycle: Archived, Generation: 4}
	archived := NewReadVersions(state)
	if got, _ := archived.Source(path); got == source || archived.Session("one") == replacement.Session("one") {
		t.Fatal("archive did not invalidate source and session readers")
	}
}

func TestReadVersionsPreserveUnknownFieldsAndMappingOnlySources(t *testing.T) {
	state := newState()
	path := filepath.Join(t.TempDir(), "old.jsonl")
	state.SourceMappings["old-writer"] = SourceMapping{Path: path, SessionID: "legacy"}
	first := NewReadVersions(state)
	if first.Session("legacy") == "" {
		t.Fatal("mapping without explicit lifecycle has no token")
	}
	mapping := state.SourceMappings["old-writer"]
	mapping.extra = map[string]json.RawMessage{"future-proof": json.RawMessage(`"changed"`)}
	state.SourceMappings["old-writer"] = mapping
	second := NewReadVersions(state)
	if first.Session("legacy") == second.Session("legacy") {
		t.Fatal("unknown source proof was omitted from invalidation")
	}
	delete(state.SourceMappings, "old-writer")
	removed := NewReadVersions(state)
	if version, err := removed.Source(path); version != "" || err != nil || removed.Session("legacy") != "" {
		t.Fatalf("removed mapping retained a token: %q %v", version, err)
	}
}
