package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestHistoryReadersAcceptLegacyImportMetadata(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "imported"})
	if err != nil {
		t.Fatal(err)
	}
	// Use the wire fields, not the shared decoder type: this fixture guards
	// the actual import contract independently of its implementation.
	payload := json.RawMessage(`{"source":{},"messages":[{"id":"imported-message","role":"user","content":"imported question"}],"goal":{"goal":"example"},"modelRef":"test","modelIdentity":"test-model"}`)
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "import", Events: []Event{{Kind: "legacy/import", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	page := historyPageReady(t, service.Query(), runtime.Ref(), "", 10)
	if len(page.Messages) != 1 || page.Messages[0].MessageID != "imported-message" {
		t.Fatalf("imported history: %+v", page)
	}
	if page := searchHistoryReady(t, service.Query(), runtime.Ref(), "imported", "", 10); len(page.Hits) != 1 {
		t.Fatalf("imported search: %+v", page)
	}
}

func TestReplacementEventSchemaRemainsStrict(t *testing.T) {
	for _, kind := range []string{"history/replace", "legacy/import"} {
		for _, payload := range []string{`{"messages":[],"typo":true}`, `{"messages":null}`, `{}`} {
			event := Event{Kind: kind, Payload: json.RawMessage(payload)}
			if _, err := replacementEventMessages(event, event.Payload); !errors.Is(err, ErrDamagedStore) {
				t.Fatalf("%s accepted invalid payload %s: %v", kind, payload, err)
			}
		}
		messages, err := replacementEventMessages(Event{Kind: kind}, json.RawMessage(`{"messages":[]}`))
		if err != nil || messages == nil || len(messages) != 0 {
			t.Fatalf("%s rejected valid empty history: %v", kind, err)
		}
	}
}
