package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/attachment"
	"reasonix/internal/config"
	"reasonix/internal/provider"
)

func TestHistoricalImageLossDoesNotBlockTextOrHealthyImages(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt", "metadata"} {
		t.Run(damage, func(t *testing.T) {
			root := t.TempDir()
			c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionPath: filepath.Join(root, "session.jsonl"), ImageRouteConfig: config.Default()})
			draft, err := c.StageImage(t.Context(), "old.png", "image/png", "data:image/png;base64,"+tinyPNG)
			if err != nil {
				t.Fatal(err)
			}
			digest := draft.Ref.Content.Digest
			object := filepath.Join(c.attachmentService().Store().Root(), "objects", digest[:2], digest[2:4], digest)
			switch damage {
			case "missing":
				err = os.Remove(object)
			case "corrupt":
				err = os.WriteFile(object, []byte("corrupt"), 0o600)
			case "metadata":
				draft.Ref.Width = 0
			}
			if err != nil {
				t.Fatal(err)
			}
			messages := []provider.Message{
				{ID: "old", Role: provider.RoleUser, Content: "old text", ImageInputs: []attachment.ImageInput{
					{Kind: attachment.KindAttachment, Attachment: &draft.Ref},
					{Kind: attachment.KindURL, URL: "https://example.invalid/healthy.png"},
				}},
				{ID: "next", Role: provider.RoleUser, Content: "continue with text"},
			}
			before, _ := json.Marshal(messages)
			got, err := c.ResolveRequestImagesForModel(t.Context(), messages, "fixture/vision", true)
			if err != nil {
				t.Fatal(err)
			}
			if len(got[0].Images) != 1 || !strings.Contains(got[0].Content, "unavailable") || got[1].Content != messages[1].Content {
				t.Fatalf("recovered messages = %+v", got)
			}
			after, _ := json.Marshal(messages)
			if !bytes.Equal(before, after) {
				t.Fatal("recovery rewrote canonical image references")
			}
			if _, err := c.ResolveRequestImagesForModel(t.Context(), messages[:1], "fixture/vision", true); err == nil {
				t.Fatal("current-turn image failure was ignored")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := c.ResolveRequestImagesForModel(ctx, messages, "fixture/vision", true); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was swallowed: %v", err)
			}
		})
	}
}

func TestImageRouteConfigurationErrorIsNotCached(t *testing.T) {
	root := t.TempDir()
	// NUL makes path resolution fail on every supported OS. A regular file
	// used as a parent is ENOTDIR on Unix but missing-path fallback on Windows.
	c := &Controller{workspaceRoot: filepath.Join(root, "invalid\x00root")}
	if _, err := c.imageRequestRoute("custom/vision-pro"); err == nil {
		t.Fatal("unreadable configuration accepted")
	}
	// Correct the source and retry on the same controller: the failed load
	// must not mark its route snapshot ready or memoize the error.
	c.workspaceRoot = root
	writeVisionTestConfig(t, root)
	got, err := c.imageRequestRoute("custom/vision-pro")
	if err != nil || got.BaseURL != "https://example.invalid/v1" {
		t.Fatalf("corrected config remained blocked: %+v, %v", got, err)
	}
}
