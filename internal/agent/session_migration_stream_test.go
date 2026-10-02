package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/store"
)

func TestStreamSessionMessagesForMigrationAppliesReplaceWithoutRetainingPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	records := []sessionEventRecord{
		{SchemaVersion: 1, Type: sessionEventTypeReplace, Messages: []provider.Message{{Role: provider.RoleSystem, Content: "system"}, {Role: provider.RoleUser, Content: "old"}}},
		{SchemaVersion: 1, Type: sessionEventTypeAppend, MessageIndex: 2, Messages: []provider.Message{{Role: provider.RoleAssistant, Content: "obsolete"}}},
		{SchemaVersion: 1, Type: sessionEventTypeReplace, Messages: []provider.Message{{Role: provider.RoleUser, Content: "final"}}},
		{SchemaVersion: 1, Type: sessionEventTypeAppend, MessageIndex: 1, Messages: []provider.Message{{Role: provider.RoleAssistant, Content: "answer"}}},
	}
	file, err := os.Create(store.SessionEventLog(path))
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var messages []provider.Message
	resets := 0
	result, err := StreamSessionMessagesForMigration(t.Context(), path, "", func() error {
		resets++
		messages = nil
		return nil
	}, func(message provider.Message) error {
		messages = append(messages, message)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.FromEvents || result.Messages != 2 || resets != 3 {
		t.Fatalf("result=%+v resets=%d", result, resets)
	}
	if len(messages) != 2 || messages[0].Content != "final" || messages[1].Content != "answer" || messages[0].ID == "" || messages[1].ID == "" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestStreamSessionMessagesForMigrationRejectsBrokenAppendChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	record := sessionEventRecord{SchemaVersion: 1, Type: sessionEventTypeAppend, MessageIndex: 7, Messages: []provider.Message{{Role: provider.RoleUser, Content: "lost"}}}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(store.SessionEventLog(path), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := StreamSessionMessagesForMigration(t.Context(), path, "", func() error { return nil }, func(provider.Message) error { return nil })
	if !errors.Is(err, ErrSessionHistoryDamaged) {
		t.Fatalf("error = %v", err)
	}
}
