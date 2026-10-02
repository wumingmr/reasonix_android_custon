package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"reasonix/internal/provider"
)

func TestCatalogPreviewUsesAuthoredTextBeforeTruncation(t *testing.T) {
	const task = "修复左侧会话标题"
	wrapped := "<response-language>" + strings.Repeat("internal preference ", 40) + "</response-language>\n<capability-route version=\"1\">tools</capability-route>\n" + task
	for _, tc := range []struct{ name, content, raw, want string }{
		{"legacy", wrapped, "", task},
		{"raw", wrapped, "用户实际输入", "用户实际输入"},
		{"raw markup is authored", wrapped, "<response-language>literal example</response-language>", "<response-language>literal example</response-language>"},
		{"raw only", "", task, task},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := provider.Message{Role: provider.RoleUser, Content: tc.content, RawContent: tc.raw, Origin: provider.MessageOriginUser}
			if got := messagePreview(message); got != tc.want {
				t.Fatalf("preview = %q, want %q", got, tc.want)
			}
			metadata := metadataFromProjection(Manifest{}, 0, Projection{Title: "My custom title", Messages: []provider.Message{
				{Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "host context"}, message,
			}})
			if metadata.Title != "My custom title" || metadata.Preview != tc.want {
				t.Fatalf("metadata = %+v", metadata)
			}
		})
	}
}

func TestCatalogPreviewSurvivesColdReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "preview")
	s, err := CreateWithOptions(dir, "preview", OpenOptions{ExternalHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, message := range []provider.Message{
		{ID: "host", Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "host context"},
		{ID: "user", Role: provider.RoleUser, Content: "<reasoning-language>" + strings.Repeat("internal", 80) + "</reasoning-language>\n实际请求"},
	} {
		payload, _ := json.Marshal(map[string]any{"message": message})
		if _, err := s.Append(t.Context(), Batch{OperationID: string(rune('a' + i)), Events: []Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := s.CatalogMetadata().Preview; got != "实际请求" {
		t.Fatalf("live preview = %q", got)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-fix checkpoint: its truncated prefix has lost the task.
	db, err := bolt.Open(filepath.Join(recoveryCacheDir(dir), recoveryDBName), 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recoveryCheckpointBucket)
		for _, key := range [][]byte{recoveryCurrentKey, recoveryPreviousKey} {
			if data := bucket.Get(key); data != nil {
				var checkpoint recoveryCheckpoint
				if err := decodeRecoveryValue(data, &checkpoint); err != nil {
					return err
				}
				checkpoint.ProjectionVersion = 2
				checkpoint.CatalogPreview = "<reasoning-language>internal..."
				encoded, err := encodeRecoveryValue(checkpoint)
				if err != nil {
					return err
				}
				if err := bucket.Put(key, encoded); err != nil {
					return err
				}
			}
		}
		return nil
	})
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	var stats RecoveryOpenStats
	reopened, err := OpenWithOptions(dir, "preview", OpenOptions{ExternalHistory: true, ObserveRecovery: func(got RecoveryOpenStats) { stats = got }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	if stats.UsedCheckpoint {
		t.Fatal("old display checkpoint was reused")
	}
	if got := reopened.CatalogMetadata().Preview; got != "实际请求" {
		t.Fatalf("cold preview = %q", got)
	}
}

func TestCatalogRejectsOldDisplayProjection(t *testing.T) {
	dir := t.TempDir()
	manifest := Manifest{SessionID: "old-preview", CreatedAt: time.Unix(1, 0)}
	metadata := metadataFromProjection(manifest, 0, Projection{})
	metadata.Version = 1
	metadata.Preview = "<response-language>" + strings.Repeat("internal", 40)
	if err := writeCatalogMetadata(dir, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := readCatalogMetadata(dir, manifest, logRevision{}); err == nil {
		t.Fatal("old truncated preview was reused")
	}
}

func TestMessagePreviewDoesNotExposeHostProtocolBeforeHydration(t *testing.T) {
	message := provider.Message{Role: provider.RoleUser, Origin: provider.MessageOriginHost,
		Content: "<session-context version=\"1\">" + strings.Repeat("environment", 4000) + "</session-context>", RawContent: "host-only metadata"}
	if got := messagePreview(message); got != "" {
		t.Fatalf("host preview = %q", got)
	}
	message.Origin = provider.MessageOriginUser
	message.RawContent = "用户引用的上下文"
	if got := messagePreview(message); got != message.RawContent {
		t.Fatalf("authored preview = %q", got)
	}
	message.Origin = ""
	message.RawContent = ""
	message.Content = "<session-context version=\"1\">\nThis host-generated snapshot supersedes every earlier session-context snapshot.\n" + strings.Repeat("environment", 4000)
	if got := messagePreview(message); got != "" {
		t.Fatalf("legacy host preview = %q", got)
	}
}
