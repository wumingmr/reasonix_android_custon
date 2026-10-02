package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func TestNativePairedHistoryResolutionPreservesNewestSource(t *testing.T) {
	message := func(text string) provider.Message {
		return provider.Message{ID: text, Role: provider.RoleUser, Content: text}
	}
	for _, tc := range []struct {
		name            string
		legacy, stored  []provider.Message
		found, conflict bool
	}{
		{"stored-newer", []provider.Message{message("a")}, []provider.Message{message("a"), message("b")}, true, false},
		{"legacy-newer", []provider.Message{message("a"), message("b")}, []provider.Message{message("a")}, false, false},
		{"equal", []provider.Message{message("a")}, []provider.Message{message("a")}, true, false},
		{"diverged", []provider.Message{message("a")}, []provider.Message{message("b")}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "prototype.jsonl")
			dir := filepath.Join(root, "stores", agent.BranchID(path))
			payload, _ := json.Marshal(map[string]any{"messages": tc.stored})
			writePrototypeStore(t, dir, []Event{{Kind: "context/replace", Payload: payload}}, "")
			manifestPath := filepath.Join(dir, "manifest.json")
			manifest, err := readStoredManifest(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			manifest.SessionID = agent.BranchID(path)
			if err := writeManifestFile(manifestPath, manifest); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(manifestPath)
			service, err := NewService("local", NewFilesystemPersistence(filepath.Dir(dir)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseAll(t.Context()) })
			ref, found, err := service.ExistingCanonicalForLegacy(path, agent.NewSession("").CloneWithMessages(tc.legacy))
			if found != tc.found || errors.Is(err, ErrImportConflict) != tc.conflict || err != nil && !tc.conflict {
				t.Fatalf("resolution = %+v %v %v", ref, found, err)
			}
			after, _ := os.ReadFile(manifestPath)
			if !bytes.Equal(before, after) {
				t.Fatal("read-only resolution changed the source")
			}
			if _, open := service.Runtime(SessionRef{HostID: "local", SessionID: agent.BranchID(path)}); open {
				t.Fatal("resolution acquired a writer")
			}
		})
	}
}
