package sessioninbox

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPreparedQueueCrossWriterEditAndMove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	s, err := Open(path, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(path, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	enqueue := func(text string, intent InboxIntent) string {
		t.Helper()
		r, err := s.Enqueue(EnqueueRequest{Intent: intent, Envelope: PromptEnvelope{SubmitText: text, DisplayText: text}})
		if err != nil {
			t.Fatal(err)
		}
		return r.ItemID
	}
	a, b := enqueue("first", IntentSteer), enqueue("second", IntentFollowup)
	if head, _ := s.NextQueued(); head.ID != a {
		t.Fatal("hidden intent priority reordered queue")
	}
	prepared, env, _ := s.ReadItem(a)
	env.SubmitText, env.RawText, env.DisplayText = "  edited\nbody  ", "  edited\nbody  ", "  edited\nbody  "
	updated, err := other.UpdateItemIfVersion(a, env, ContentVersion(prepared))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionPrepared(a, ContentVersion(prepared), StateRunning, "", true); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("stale body claim: %v", err)
	}
	if _, err := s.UpdateItemIfVersion(a, env, ContentVersion(prepared)); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("stale save: %v", err)
	}
	if err := other.MoveItemBefore(b, &a, other.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionPrepared(a, ContentVersion(updated), StateRunning, "", true); !errors.Is(err, ErrOrderChanged) {
		t.Fatalf("stale head claim: %v", err)
	}
	if err := s.TransitionPrepared(a, ContentVersion(updated), StateBlocked, "stale failure", true); !errors.Is(err, ErrOrderChanged) {
		t.Fatalf("stale preparation failure: %v", err)
	}
	metaB, _, _ := other.ReadItem(b)
	if err := s.TransitionPrepared(b, ContentVersion(metaB), StateRunning, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := other.UpdateItemIfVersion(b, env, ContentVersion(metaB)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("edited running item: %v", err)
	}
	_, actual, _ := s.ReadItem(a)
	if actual.SubmitText != env.SubmitText {
		t.Fatalf("text was normalized: %q", actual.SubmitText)
	}
}

func TestQueuePauseUncertainAndPersistentAnchors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	s, err := Open(path, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, text := range []string{"a", "b", "c"} {
		r, err := s.Enqueue(EnqueueRequest{Envelope: PromptEnvelope{SubmitText: text}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ItemID)
	}
	if err := s.SetState(ids[0], StateUncertain, "unknown"); err != nil {
		t.Fatal(err)
	}
	if _, found := s.NextQueued(); found {
		t.Fatal("bypassed uncertain head")
	}
	if err := s.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	meta, env, _ := s.ReadItem(ids[0])
	env.SubmitText = "edited uncertain"
	if _, err := s.UpdateItemIfVersion(ids[0], env, ContentVersion(meta)); err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); !snap.Paused || snap.Items[0].State != StateUncertain {
		t.Fatal("edit resumed uncertain delivery")
	}
	rev := s.Snapshot().Revision
	missing := "missing"
	if err := s.MoveItemBefore(ids[0], &missing, rev); !errors.Is(err, ErrAnchorMissing) {
		t.Fatal(err)
	}
	if err := s.MoveItemBefore(ids[0], nil, rev); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveItemBefore(ids[1], nil, rev); !errors.Is(err, ErrOrderChanged) {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap := s.Snapshot()
	if !snap.Paused || snap.Items[0].ID != ids[1] || snap.Items[2].ID != ids[0] {
		t.Fatalf("order not durable: %+v", snap)
	}
}

func TestVersionedAppendCannotOverwriteEditAndReplaysAlias(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.jsonl"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	receipt, err := s.Enqueue(EnqueueRequest{Envelope: PromptEnvelope{SubmitText: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	meta, env, _ := s.ReadItem(receipt.ItemID)
	version := ContentVersion(meta)
	env.SubmitText = "edited"
	updated, err := s.UpdateItemIfVersion(meta.ID, env, version)
	if err != nil {
		t.Fatal(err)
	}
	alias := PromptEnvelope{SubmitText: "append", Source: "bot"}
	env.SubmitText = "original\nappend"
	if _, err := s.UpdateItemWithIdempotencyIfVersion(meta.ID, env, "append-1", alias, version); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("stale append overwrote edit: %v", err)
	}
	env.SubmitText = "edited\nappend"
	version = ContentVersion(updated)
	if _, err := s.UpdateItemWithIdempotencyIfVersion(meta.ID, env, "append-1", alias, version); err != nil {
		t.Fatal(err)
	}
	env.SubmitText += "\nappend"
	if _, err := s.UpdateItemWithIdempotencyIfVersion(meta.ID, env, "append-1", alias, version); err != nil {
		t.Fatalf("lost-response retry must replay alias: %v", err)
	}
	_, stored, _ := s.ReadItem(meta.ID)
	if stored.SubmitText != "edited\nappend" {
		t.Fatalf("duplicated append: %q", stored.SubmitText)
	}
}
