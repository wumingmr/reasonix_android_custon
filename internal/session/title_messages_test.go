package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestTitleMessagesReadAuthoredHistoryAcrossRestart(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "title-history"})
	if err != nil {
		t.Fatal(err)
	}
	if messages, err := service.Query().TitleMessages(t.Context(), runtime.Ref(), 3); err != nil || len(messages) != 0 {
		t.Fatalf("empty history = %v, %v", messages, err)
	}
	want := strings.Repeat("large user text ", 80000)
	for _, message := range []provider.Message{
		{ID: "host", Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "hidden host instructions"},
		{ID: "one", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: want},
		{ID: "assistant", Role: provider.RoleAssistant, Content: "reply"},
		{ID: "two", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "second request"},
		{ID: "three", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "third request"},
		{ID: "four", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "fourth request"},
	} {
		payload, _ := json.Marshal(map[string]any{"message": message})
		if _, err := runtime.Session().AppendBatch(t.Context(), message.ID, []Event{{Kind: "message/complete", Payload: payload}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	messages, err := service.Query().TitleMessages(t.Context(), runtime.Ref(), 3)
	if err != nil || len(messages) != 3 {
		t.Fatalf("title messages = %d, %v", len(messages), err)
	}
	if messages[0].Content != want || messages[1].Content != "second request" || messages[2].Content != "third request" {
		t.Fatalf("title message identities = %q, %q, %q", messages[0].ID, messages[1].ID, messages[2].ID)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.Query().TitleMessages(cancelled, runtime.Ref(), 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled title read = %v", err)
	}
}

func TestSetTitleIfSequenceIsAtomicWithManualTitleCommit(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "title-race"})
	if err != nil {
		t.Fatal(err)
	}
	expected := uint64(0)
	prepared, err := runtime.Session().PrepareBatchContext(t.Context(), "delayed-title", Batch{Events: []Event{{Kind: "session/title", Payload: []byte(`{"title":"stale AI title"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if err := service.SetTitle(t.Context(), runtime.Ref(), "manual title"); err != nil {
		t.Fatal(err)
	}
	before := runtime.Session().EventSequence()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.SetTitle(cancelled, runtime.Ref(), "cancelled title"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled title write = %v", err)
	}
	if _, err := runtime.Session().commitPrepared(prepared, &expected); !errors.Is(err, ErrSessionTitleChanged) {
		t.Fatalf("interleaved title commit = %v", err)
	}
	if runtime.Session().EventSequence() != before {
		t.Fatal("rejected title consumed an event sequence")
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTitleIfSequence(t.Context(), runtime.Ref(), 0, "stale AI title"); !errors.Is(err, ErrSessionTitleChanged) {
		t.Fatalf("cold stale title = %v", err)
	}
	info, err := service.Query().Stat(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetTitleIfSequence(t.Context(), runtime.Ref(), info.TitleSequence, "new AI title"); err != nil {
		t.Fatal(err)
	}
	if info, err := service.Query().Stat(t.Context(), runtime.Ref()); err != nil || info.Title != "new AI title" {
		t.Fatalf("persisted title = %+v, %v", info, err)
	}
}

func TestSetTitleIfSequenceRejectsABAAndSameValueManualSave(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "title-aba"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetTitle(t.Context(), runtime.Ref(), "A"); err != nil {
		t.Fatal(err)
	}
	original := runtime.Session().Snapshot().Projection.TitleSequence
	if err := service.SetTitle(t.Context(), runtime.Ref(), "B"); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTitle(t.Context(), runtime.Ref(), "A"); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTitleIfSequence(t.Context(), runtime.Ref(), original, "stale AI"); !errors.Is(err, ErrSessionTitleChanged) {
		t.Fatalf("ABA title commit = %v", err)
	}
	current := runtime.Session().Snapshot().Projection.TitleSequence
	if err := service.SetTitle(t.Context(), runtime.Ref(), "A"); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTitleIfSequence(t.Context(), runtime.Ref(), current, "stale same-value AI"); !errors.Is(err, ErrSessionTitleChanged) {
		t.Fatalf("same-value title commit = %v", err)
	}
}
