package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestTaggedFormalSessionsKeepHeaderAndProviderHistory(t *testing.T) {
	for _, version := range []string{"1.38.9", "1.38.10", "1.38.11"} {
		t.Run(version, func(t *testing.T) {
			fixture := taggedHistoryFixture(t, version)
			root := filepath.Join(t.TempDir(), "by-id")
			copyTaggedDirectory(t, filepath.Join(fixture, "formal"), root)
			var want []provider.Message
			readTaggedJSON(t, filepath.Join(fixture, "canonical-expected.json"), &want)
			ref := session.SessionRef{HostID: "desktop", SessionID: "formal-session"}
			for restart := range 2 {
				service, err := session.NewService("desktop", session.NewFilesystemPersistence(root))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = service.Shutdown(t.Context()) })
				info, err := service.Query().Stat(t.Context(), ref)
				if err != nil || info.CWD != "/synthetic/workspace" || info.SessionID != ref.SessionID {
					t.Fatalf("formal header changed: %+v %v", info, err)
				}
				snapshot, err := service.Query().Snapshot(t.Context(), ref)
				if err != nil {
					t.Fatal(err)
				}
				assertTaggedMessages(t, snapshot.Projection.Messages, want)
				if restart == 0 {
					binding, err := service.Open(t.Context(), ref)
					if err != nil {
						t.Fatal(err)
					}
					message := provider.Message{ID: "after-upgrade", Role: provider.RoleUser, Content: "New input after direct formal upgrade"}
					payload, err := json.Marshal(map[string]any{"message": message})
					if err != nil {
						t.Fatal(err)
					}
					_, err = binding.Runtime().Session().AppendBatch(t.Context(), "after-upgrade", []session.Event{{Kind: "message/complete", Payload: payload}})
					if err != nil {
						t.Fatal(err)
					}
					if err := binding.Release(t.Context()); err != nil {
						t.Fatal(err)
					}
					want = append(want, message)
				}
				if err := service.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
