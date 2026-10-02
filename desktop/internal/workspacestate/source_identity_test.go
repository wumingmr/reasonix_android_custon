package workspacestate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSourceIdentityAliasesSurviveReopenWithoutRewritingReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MixedCase", "history")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	physical, err := sourcePathKey(path)
	if err != nil {
		t.Fatal(err)
	}
	current := func(head string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(physical+"\x00"+head))) }
	state := newState()
	// Simulate durable pre-normalization keys on every CI filesystem.
	for _, head := range []string{"", "main", "fork"} {
		key := "old-" + head
		state.SourceMappings[key] = SourceMapping{SourceKey: key, Path: path, HeadID: head, SessionID: "session-" + head, WorkspaceID: GlobalWorkspaceID, Fingerprint: "fingerprint"}
	}
	version := state.SourceMappings["old-main"]
	version.SourceKey, version.SessionID = "old-main:review:version-1", "reviewed"
	state.SourceMappings[version.SourceKey] = version
	pending := SourceMapping{SourceKey: "old-pending", Path: path, HeadID: "pending"}
	state.PendingOperations["pending"] = Operation{ID: "pending", Mapping: &pending, Kind: "import", Phase: "content_ready", Lifecycle: Active}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store := NewStore(file)
		for _, project := range []bool{false, true} {
			view, err := store.loadSnapshot(t.Context(), project)
			if err != nil {
				t.Fatal(err)
			}
			for _, head := range []string{"", "main", "fork"} {
				mapping, found, err := view.ResolveSource(current(head))
				if err != nil || !found || mapping.SourceKey != "old-"+head || mapping.SessionID != "session-"+head {
					t.Fatalf("head %q resolved to %+v, %v", head, mapping, err)
				}
			}
			mapping, found, err := view.ResolveSource(current("main") + ":review:version-1")
			if err != nil || !found || mapping.SessionID != "reviewed" {
				t.Fatalf("reviewed version: %+v %v", mapping, err)
			}
			if _, found, _ := view.ResolveSource(current("unadopted")); found {
				t.Fatal("independent head was consumed")
			}
			if _, found, _ := view.ResolveSource(current("main") + ":review:version-2"); found {
				t.Fatal("new version was consumed")
			}
			if !slices.Contains(view.SourceKeys("old-pending"), current("pending")) {
				t.Fatal("pending operation lost its alias")
			}
			keys := view.SourceKeys("old-main")
			keys[0] = "changed"
			if view.SourceKeys("old-main")[0] != "old-main" {
				t.Fatal("caller changed cached aliases")
			}
		}
		snapshot, err := store.VerifySnapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		mapping, found, err := snapshot.ResolveSource(current(""))
		if err != nil || !found || mapping.SourceKey != "old-" {
			t.Fatalf("execution snapshot: %+v %v", mapping, err)
		}
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(after, body) {
			t.Fatal("read aliases rewrote persisted receipts")
		}
	}
}

func TestSourceIdentityAliasesRejectAmbiguousOwnership(t *testing.T) {
	path := t.TempDir()
	physical, err := sourcePathKey(path)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(physical+"\x00")))
	state := newState()
	for _, id := range []string{"first", "second"} {
		state.SourceMappings[id] = SourceMapping{SourceKey: id, Path: path, SessionID: id}
	}
	if _, found, err := state.ResolveSource(key); found || !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("ambiguous source: found=%v err=%v", found, err)
	}
	if mapping, found, err := state.ResolveSource("first"); err != nil || !found || mapping.SessionID != "first" {
		t.Fatal("exact durable identity was lost")
	}
}

func TestConvertedDirectoryIdentityIncludesHeadlessDiscovery(t *testing.T) {
	path := t.TempDir()
	physical, err := sourcePathKey(path)
	if err != nil {
		t.Fatal(err)
	}
	key := func(head string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(physical+"\x00"+head))) }
	for _, format := range []string{"canonical", "legacy"} {
		t.Run(format, func(t *testing.T) {
			state := newState()
			mapping := SourceMapping{SourceKey: "previous-head-key", Path: path, HeadID: "main", Format: format, SessionID: "adopted"}
			state.SourceMappings[mapping.SourceKey] = mapping
			mapped, found, err := state.ResolveSource(key(""))
			if err != nil || found != (format == "canonical") || (found && mapped.SessionID != "adopted") {
				t.Fatalf("headless discovery: %+v %v %v", mapped, found, err)
			}
			if _, found, _ := state.ResolveSource(key("fork")); found {
				t.Fatal("independent head became an alias")
			}
			delete(state.SourceMappings, mapping.SourceKey)
			mapping.SourceKey += ":review:version-1"
			state.SourceMappings[mapping.SourceKey] = mapping
			if _, found, _ := state.ResolveSource(key("")); found {
				t.Fatal("reviewed version consumed its parent identity")
			}
			_, found, err = state.ResolveSource(key("") + ":review:version-1")
			if err != nil || found != (format == "canonical") {
				t.Fatalf("reviewed directory identity: %v %v", found, err)
			}
			delete(state.SourceMappings, mapping.SourceKey)
			state.PendingOperations["interrupted"] = Operation{ID: "interrupted", Mapping: &mapping}
			if got := slices.Contains(state.SourceKeys(mapping.SourceKey), key("")+":review:version-1"); got != (format == "canonical") {
				t.Fatal("interrupted conversion lost its directory identity")
			}
		})
	}
	state := newState()
	for _, head := range []string{"main", "fork"} {
		state.SourceMappings[head] = SourceMapping{SourceKey: head, Path: path, HeadID: head, Format: "canonical", SessionID: head}
	}
	if _, found, err := state.ResolveSource(key("")); found || !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("ambiguous converted directory chose a target: %v %v", found, err)
	}
}
