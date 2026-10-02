package sessioninbox

import (
	"path/filepath"
	"testing"

	"reasonix/internal/store"
)

func TestOpenAtSeparatesLocatorFromStorageWithoutChangingFormat(t *testing.T) {
	storagePath := filepath.Join(t.TempDir(), "runtime-inbox.jsonl")
	dir := store.SessionInboxDir(storagePath)
	const locator = "session-id:canonical.with.dots"
	s, err := OpenAt(locator, dir, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	receipt, err := s.Enqueue(EnqueueRequest{Intent: IntentFollowup, Idempotency: "request", Envelope: PromptEnvelope{SubmitText: "queued"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionPath() != locator || s.Snapshot().SessionPath != locator || s.Dir() != dir {
		t.Fatal("logical locator was replaced by its storage path")
	}
	if meta, _, err := s.ReadItem(receipt.ItemID); err != nil || meta.SessionID != "canonical.with.dots" {
		t.Fatalf("default metadata lost the canonical session identity: %+v %v", meta, err)
	}
	s.Close()
	// An old path-based reader can still read the same manifest and blobs.
	legacy, err := Open(storagePath, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if got, found := legacy.LookupReceipt("request"); !found || got.ItemID != receipt.ItemID {
		t.Fatalf("legacy reader lost receipt: %+v %v", got, found)
	}
	if _, env, err := legacy.ReadItem(receipt.ItemID); err != nil || env.SubmitText != "queued" {
		t.Fatalf("legacy reader lost body: %+v %v", env, err)
	}
}
