package workspacestate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// reporterState registers one source file under three keys by three spellings
// of its path, owned by two sessions.
func reporterState(t *testing.T, liveSecond bool) (State, string, func(string) string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "sessions", "20260919-053801.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	physical, err := sourcePathKey(source)
	if err != nil {
		t.Fatal(err)
	}
	spelled := filepath.Join(dir, "sessions", "..", "sessions", "20260919-053801.jsonl")
	key := func(head string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(physical+"\x00"+head))) }
	state := newState()
	add := func(sourceKey, path, head, session string) {
		state.SourceMappings[sourceKey] = SourceMapping{
			SourceKey: sourceKey, Path: path, HeadID: head, Format: "canonical",
			Fingerprint: "fp", SessionID: session, WorkspaceID: GlobalWorkspaceID,
		}
	}
	add("legacy-lowercase", spelled, "", "kept")
	add("legacy-main", source, "main", "duplicate")
	add("legacy-ulid", source, "01M2W7FH2V02S5E06JPVZYMFNT", "kept")
	state.SessionStates["kept"] = SessionState{Lifecycle: Active, Generation: 64}
	state.SessionStates["duplicate"] = SessionState{Lifecycle: Archived, Generation: 91}
	if liveSecond {
		state.SessionStates["duplicate"] = SessionState{Lifecycle: Active, Generation: 91}
	}
	return state, source, key
}

func TestDuplicateSourceMappingsResolveToTheLiveOwner(t *testing.T) {
	state, _, key := reporterState(t, false)
	file := filepath.Join(t.TempDir(), "state.json")
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	view, err := NewStore(file).loadSnapshot(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	mapping, found, err := view.ResolveSource(key(""))
	if err != nil || !found || mapping.SessionID != "kept" {
		t.Fatalf("resolved %+v found=%v err=%v, want the live session", mapping, found, err)
	}
	if after, err := os.ReadFile(file); err != nil || !bytes.Equal(after, body) {
		t.Fatal("resolving duplicates rewrote the persisted receipts")
	}
	if len(view.SourceMappings) != 3 {
		t.Fatal("a duplicate receipt was dropped")
	}
}

func TestDuplicateSourceMappingsStayAmbiguousWithoutOneLiveOwner(t *testing.T) {
	both, _, key := reporterState(t, true)
	if _, found, err := both.ResolveSource(key("")); found || !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("two live owners: found=%v err=%v", found, err)
	}
	moved, _, key := reporterState(t, false)
	duplicate := moved.SourceMappings["legacy-main"]
	duplicate.Fingerprint = "other"
	moved.SourceMappings["legacy-main"] = duplicate
	moved.SessionStates["duplicate"] = SessionState{Lifecycle: Active}
	if _, found, err := moved.ResolveSource(key("")); found || !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("different content: found=%v err=%v", found, err)
	}
	none, _, key := reporterState(t, false)
	none.SessionStates["kept"] = SessionState{Lifecycle: Archived}
	if _, found, err := none.ResolveSource(key("")); found || !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("no live owner: found=%v err=%v", found, err)
	}
}

func TestDuplicateSourceSettlementLeavesRetiredReceiptsAddressable(t *testing.T) {
	state, _, key := reporterState(t, false)
	mapping, _, _ := state.ResolveSource(key(""))
	if mapping.SessionID == "duplicate" {
		t.Fatal("lookup by alias resolved to the retired copy")
	}
	retired, found, err := state.ResolveSource("legacy-main")
	if err != nil || !found || retired.SessionID != "duplicate" || retired.HeadID != "main" {
		t.Fatalf("exact durable key resolved %+v found=%v err=%v", retired, found, err)
	}
	if mapping.HeadID == retired.HeadID {
		t.Fatal("alias lookup returned the retired copy's head, so head-checked callers would act on it")
	}
}
