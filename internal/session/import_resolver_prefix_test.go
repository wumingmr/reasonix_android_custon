package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestCompareLegacySpoolPreservesBothPrefixDirections(t *testing.T) {
	message := func(id string) provider.Message {
		return provider.Message{ID: id, Role: provider.RoleUser, Content: id}
	}
	for _, test := range []struct {
		name            string
		legacy, preview []provider.Message
		want            importMessageRelation
	}{
		{"equal", []provider.Message{message("a")}, []provider.Message{message("a")}, importMessagesEqual},
		{"newer-events", []provider.Message{message("a")}, []provider.Message{message("a"), message("b")}, importLegacyPrefix},
		{"newer-transcript", []provider.Message{message("a"), message("b")}, []provider.Message{message("a")}, importPreviewPrefix},
		{"divergent-longer-transcript", []provider.Message{message("a"), message("c"), message("d")}, []provider.Message{message("a"), message("b")}, importMessagesConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "messages.jsons")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range test.legacy {
				if err := json.NewEncoder(file).Encode(message); err != nil {
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			got, count, _, err := compareLegacySpool(path, test.preview)
			if err != nil || got != test.want || count != len(test.legacy) {
				t.Fatalf("relation=%v count=%d err=%v", got, count, err)
			}
		})
	}
}
