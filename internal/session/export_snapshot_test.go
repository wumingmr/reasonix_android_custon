package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reasonix/internal/provider"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlushThroughDoesNotWaitForLaterWrites(t *testing.T) {
	first, second := make(chan struct{}), make(chan struct{})
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	s, err := OpenWithOptions(filepath.Join(t.TempDir(), "session"), "s", OpenOptions{Write: func(ctx context.Context, w io.Writer, data []byte) error {
		switch writes.Add(1) {
		case 1:
			close(first)
			<-releaseFirst
		case 2:
			close(second)
			<-releaseSecond
		}
		return writeAllContext(ctx, w, data)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	defer close(releaseSecond)
	if _, err = s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.FlushThrough(t.Context(), 1); done <- err }()
	<-first
	if _, err = s.Append(t.Context(), Batch{OperationID: "two", Events: []Event{{Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	<-second
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot waited for a later commit")
	}
}

func TestExportSnapshotIncludesAllPagesAndExcludesLaterAppends(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseAll(context.Background())
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "export"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 137)
	for i := range ids {
		ids[i] = fmt.Sprintf("m%d", i)
	}
	appendWindowMessages(t, runtime, ids...)
	snapshot, err := service.Query().CaptureExportSnapshot(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	appendWindowMessages(t, runtime, "later")
	var got []string
	err = service.Query().VisitExportMessages(t.Context(), snapshot, func(message PersistentMessage) error { got = append(got, message.MessageID); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !idsEqual(ids, got) {
		t.Fatalf("export got %d records, want %d", len(got), len(ids))
	}
}

func TestToolObservationResolvesOutsideResidentWindow(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseAll(context.Background())
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "tool-pages"})
	if err != nil {
		t.Fatal(err)
	}
	appendMessage := func(m provider.Message) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"message": m})
		if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: m.ID, Events: []Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage(provider.Message{ID: "call", Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "tool-id", Name: "bash", Arguments: "{}"}}})
	ids := make([]string, 110)
	for i := range ids {
		ids[i] = fmt.Sprintf("gap-%d", i)
	}
	appendWindowMessages(t, runtime, ids...)
	appendMessage(provider.Message{ID: "result", Role: provider.RoleTool, ToolCallID: "tool-id", Content: "OK", ToolRunState: provider.ToolRunCompleted})
	if _, err = runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Prepare deterministically: this test exercises cross-page evidence, not the
	// background locator scheduler or its UI polling deadline.
	if _, _, err := service.Query().prepareHistoryIndex(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	page := windowReady(t, service.Query(), runtime.Ref(), HistoryWindowRequest{Anchor: "message", MessageID: "call", Direction: "newer", Limit: 1})
	observed := page.Messages[0].ToolObservations["tool-id"]
	if observed.State != "completed" || observed.MessageID != "result" || observed.ContentRef == nil {
		t.Fatalf("missing out-of-window evidence: %+v", observed)
	}
}

func TestToolObservationUsesStartedEvidenceBeforeResult(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseAll(context.Background())
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "running-tool"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "call", Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "tool", Name: "bash", Arguments: "{}"}}}})
	if _, err = runtime.Session().AppendBatch(t.Context(), "call", []Event{{Kind: "message/complete", Payload: data}, {Kind: "tool/call", Payload: json.RawMessage(`{"id":"tool","name":"bash"}`)}, {Kind: "tool/start", Payload: json.RawMessage(`{"id":"tool","name":"bash"}`)}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Query().CaptureExportSnapshot(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	err = service.Query().VisitExportMessages(t.Context(), snapshot, func(message PersistentMessage) error {
		if observed := message.ToolObservations["tool"]; observed.State != "running" || observed.MessageID != "" {
			t.Fatalf("lost running evidence: %+v", observed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFlushThroughCancellationDoesNotCancelSharedWrite(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, err := OpenWithOptions(filepath.Join(t.TempDir(), "session"), "s", OpenOptions{Write: func(ctx context.Context, w io.Writer, data []byte) error {
		close(entered)
		<-release
		return writeAllContext(ctx, w, data)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if _, err = s.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "diagnostic", Payload: json.RawMessage(`{}`)}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := s.FlushThrough(ctx, 1); done <- err }()
	<-entered
	cancel()
	err = <-done
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel returned %v", err)
	}
	receipt, err := s.FlushThrough(t.Context(), 1)
	if err != nil || receipt.DurableSequence < 1 {
		t.Fatalf("cancel affected shared writer: %+v %v", receipt, err)
	}
}

func TestExportSnapshotUsesVersionedVisibleHistory(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseAll(context.Background())
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "versions"})
	if err != nil {
		t.Fatal(err)
	}
	appendWindowMessages(t, runtime, "first", "removed")
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "first", Role: provider.RoleUser, Content: "UPDATED", Origin: provider.MessageOrigin("user")}})
	if _, err = runtime.Session().AppendBatch(t.Context(), "update", []Event{{Kind: "message/upsert", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Session().AppendBatch(t.Context(), "remove", []Event{{Kind: "message/retract", Payload: json.RawMessage(`{"messageIds":["removed"]}`)}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Query().CaptureExportSnapshot(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err = service.Query().VisitExportMessages(t.Context(), snapshot, func(message PersistentMessage) error {
		count++
		var m provider.Message
		if err := json.Unmarshal(message.Inline, &m); err != nil {
			return err
		}
		if m.ID != "first" || m.Content != "UPDATED" {
			t.Fatalf("export resurrected old content: %+v", m)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("visible records=%d", count)
	}
}

func TestExportSnapshotSurvivesLaterHistoryReplacement(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseAll(context.Background())
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "replace-after-snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"original-user", "original-answer"}
	appendWindowMessages(t, runtime, ids...)
	snapshot, err := service.Query().CaptureExportSnapshot(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if current := service.Query().storageGeneration(runtime.Ref().SessionID); snapshot.StorageGeneration != current {
		t.Fatalf("snapshot generation = %q, want physical generation %q", snapshot.StorageGeneration, current)
	}
	replacement := []provider.Message{{ID: "replacement", Role: provider.RoleUser, Content: "new visible history", Origin: provider.MessageOrigin("user")}}
	payload, _ := json.Marshal(map[string]any{"messages": replacement})
	if _, err = runtime.Session().Append(t.Context(), Batch{OperationID: "replace-history", Events: []Event{{Kind: "history/replace", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err = service.Query().VisitExportMessages(t.Context(), snapshot, func(message PersistentMessage) error {
		got = append(got, message.MessageID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !idsEqual(ids, got) {
		t.Fatalf("fixed snapshot after replacement = %v, want %v", got, ids)
	}
}

func TestStreamExportCommitsStopsAtCapturedBoundary(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseAll(context.Background())
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "diagnostic-prefix"})
	if err != nil {
		t.Fatal(err)
	}
	appendWindowMessages(t, runtime, "first")
	snapshot, err := service.Query().CaptureExportSnapshot(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	appendWindowMessages(t, runtime, "later")
	if _, err = runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var last uint64
	err = service.Query().StreamExportCommits(t.Context(), snapshot, func(commit Commit) error {
		last = commit.LastSequence()
		if last > snapshot.SnapshotSequence {
			t.Fatal("export read beyond fixed boundary")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if last != snapshot.SnapshotSequence {
		t.Fatalf("got watermark %d, want %d", last, snapshot.SnapshotSequence)
	}
}
