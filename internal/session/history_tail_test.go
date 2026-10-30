package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

// HistoryTail must return the newest maxMessages entries in oldest-first order,
// and it must agree exactly with the suffix of full History(). Reading the tail
// through the locator index is what lets a TUI resume skip a whole-log replay,
// so a divergence here would silently corrupt the resumed transcript.
func TestHistoryTailMatchesFullHistorySuffix(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "tailed"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), runtime.Ref()) })
	appendWindowMessages(t, runtime, "m1", "m2", "m3", "m4", "m5")
	ref := runtime.Ref()
	query := service.Query()

	full, err := query.History(t.Context(), ref)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(full) != 5 {
		t.Fatalf("full history = %d messages, want 5", len(full))
	}

	tail, err := query.HistoryTail(t.Context(), ref, 2)
	if err != nil {
		t.Fatalf("HistoryTail: %v", err)
	}
	want := full[len(full)-2:]
	if len(tail) != len(want) {
		t.Fatalf("tail = %d messages, want %d", len(tail), len(want))
	}
	for i := range want {
		if tail[i].ID != want[i].ID || tail[i].Content != want[i].Content {
			t.Fatalf("tail[%d] = %+v, want %+v", i, tail[i], want[i])
		}
	}

	// A limit at or above the message count returns everything, still oldest-first.
	all, err := query.HistoryTail(t.Context(), ref, 50)
	if err != nil {
		t.Fatalf("HistoryTail(all): %v", err)
	}
	if len(all) != len(full) {
		t.Fatalf("tail(all) = %d messages, want %d", len(all), len(full))
	}
	for i := range full {
		if all[i].ID != full[i].ID {
			t.Fatalf("tail(all)[%d] = %q, want %q", i, all[i].ID, full[i].ID)
		}
	}

	if _, err := query.HistoryTail(t.Context(), ref, 0); err == nil {
		t.Fatal("HistoryTail accepted a non-positive limit")
	}
}

// A retracted message frees its position without renumbering, so positions can
// be non-contiguous. The tail must still return exactly maxMessages newest rows
// instead of deriving a cutoff from COUNT(*).
func TestHistoryTailToleratesPositionGaps(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "gapped"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), runtime.Ref()) })
	appendWindowMessages(t, runtime, "m1", "m2", "m3", "m4", "m5")

	// Retract the middle message, then append a new one: position 3 is retired
	// and the new message takes the next counter value, leaving a hole.
	retract, err := json.Marshal(map[string]any{"messageIds": []string{"m3"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Append(t.Context(), Batch{
		OperationID: "retract-m3",
		Events:      []Event{{Kind: "message/retract", Payload: retract}},
	}); err != nil {
		t.Fatalf("retract: %v", err)
	}
	appendWindowMessages(t, runtime, "m6")

	ref := runtime.Ref()
	query := service.Query()
	full, err := query.History(t.Context(), ref)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	tail, err := query.HistoryTail(t.Context(), ref, 3)
	if err != nil {
		t.Fatalf("HistoryTail: %v", err)
	}
	want := full[len(full)-3:]
	if len(tail) != len(want) {
		t.Fatalf("tail = %d messages, want %d (full=%d)", len(tail), len(want), len(full))
	}
	for i := range want {
		if tail[i].ID != want[i].ID {
			t.Fatalf("tail[%d] = %q, want %q", i, tail[i].ID, want[i].ID)
		}
	}
	if tail[len(tail)-1].Role != provider.RoleUser {
		t.Fatalf("tail tail role = %q", tail[len(tail)-1].Role)
	}
}
