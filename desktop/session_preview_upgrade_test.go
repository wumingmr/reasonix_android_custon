package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/session"
)

func TestDesktopStoredPreviewFormatsPreserveOriginalAndMessageIdentity(t *testing.T) {
	for _, codec := range []string{session.PrototypeCodec, session.LegacyLinearCodec, session.FinalV31Codec, session.Codec} {
		t.Run(codec, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := t.TempDir()
			path := filepath.Join(root, "historical")
			store, err := session.CreateStore(path, "historical")
			if err != nil {
				t.Fatal(err)
			}
			payload := json.RawMessage(`{"message":{"id":"unchanged-message","role":"user","content":"preview history"}}`)
			if _, err := store.Append(t.Context(), session.Batch{OperationID: "content", Events: []session.Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			commits, err := session.Replay(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(path, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest session.Manifest
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Codec, manifest.StorageRevision = codec, 0
			if codec != session.Codec {
				manifest.SchemaVersion = 3
				var log bytes.Buffer
				for _, commit := range commits {
					commit.SchemaVersion = 3
					commit.Codec = codec
					line, _ := json.Marshal(commit)
					log.Write(line)
					log.WriteByte('\n')
				}
				if err := os.WriteFile(filepath.Join(path, "events.jsonl"), log.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			body, _ = json.Marshal(manifest)
			if err := os.WriteFile(filepath.Join(path, "manifest.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := desktopSourceFingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			app := NewApp()
			app.ctx = t.Context()
			pinDesktopSessionRoot(t, app)
			source := desktopMigrationSource{root: root, scope: "global"}
			if err := app.migrateCanonicalStore(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			if err := app.migrateCanonicalStore(t.Context(), source); err != nil {
				t.Fatalf("repeat: %v", err)
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			mapping, ok := state.SourceMappings[desktopSourceKey(path, "")]
			if !ok || len(state.Workspaces["global"].SessionIDs) != 1 {
				t.Fatalf("preview not registered: %+v", state)
			}
			messages, err := app.desktopSessionService("").Query().History(t.Context(), session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID})
			if err != nil || len(messages) != 1 || messages[0].ID != "unchanged-message" || messages[0].Content != "preview history" {
				t.Fatalf("history=%+v err=%v", messages, err)
			}
			after, err := desktopSourceFingerprint(path)
			if err != nil || before != after {
				t.Fatal("historical preview was modified")
			}
		})
	}
}
