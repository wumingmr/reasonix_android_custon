package control

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/attachment"
	"reasonix/internal/config"
	"reasonix/internal/imageinput"
	"reasonix/internal/provider"
)

func writeImageGateConfig(t *testing.T, root string) {
	t.Helper()
	cfg := config.Default()
	cfg.DefaultModel = "custom/mystery"
	cfg.Providers = []config.ProviderEntry{
		{Name: "custom", Kind: "openai", BaseURL: "https://example.invalid/v1", Models: []string{"mystery"}},
		{Name: "declared", Kind: "openai", BaseURL: "https://example.invalid/v1", Models: []string{"text-only", "vision-pro"}, VisionModels: []string{"vision-pro"}},
	}
	if err := cfg.SaveTo(filepath.Join(root, "reasonix.toml")); err != nil {
		t.Fatalf("save config: %v", err)
	}
	approveWorkspace(t, root)
}

func TestImageInputIsBlockedOnlyWhenTheModelIsDeclaredTextOnly(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeImageGateConfig(t, root)
	cases := []struct {
		name, ref, fallback string
		want                bool
	}{
		{"undeclared model passes images to the provider", "custom/mystery", "", true},
		{"undeclared model keeps the configured fallback", "custom/mystery", "auto", false},
		{"declared text-only model stays blocked", "declared/text-only", "", false},
		{"declared vision model reads images", "declared/vision-pro", "", true},
		{"declared vision model reads images despite a fallback", "declared/vision-pro", "auto", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Controller{workspaceRoot: root, selection: modelSelection{ref: tc.ref}, visionModel: tc.fallback}
			if got := c.imageInputEnabled(); got != tc.want {
				t.Fatalf("imageInputEnabled(%s, fallback %q) = %v, want %v", tc.ref, tc.fallback, got, tc.want)
			}
			resolved := &Controller{workspaceRoot: root, selection: modelSelection{ref: tc.ref}, visionModel: tc.fallback,
				modelCapabilityResolver: config.NewTransientModelCapabilityResolver().Resolve}
			if got := resolved.imageInputEnabled(); got != tc.want {
				t.Fatalf("resolver imageInputEnabled(%s, fallback %q) = %v, want %v", tc.ref, tc.fallback, got, tc.want)
			}
		})
	}
}

func TestTextOnlyModelWithoutFallbackDoesNotFailOnToolOrHistoricalImages(t *testing.T) {
	root := t.TempDir()
	c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionPath: filepath.Join(root, "session.jsonl"), ImageRouteConfig: config.Default()})
	img := []attachment.ImageInput{{Kind: attachment.KindURL, URL: "https://example.invalid/shot.png"}}
	messages := []provider.Message{
		{ID: "old", Role: provider.RoleUser, Content: "old turn", ImageInputs: img},
		{ID: "now", Role: provider.RoleUser, Content: "plain text follow-up"},
		{ID: "tool", Role: provider.RoleTool, Content: "screenshot taken", ImageInputs: img},
	}
	got, err := c.ResolveRequestImagesForModel(t.Context(), messages, "declared/text-only", false)
	if err != nil {
		t.Fatalf("a plain-text turn failed because of earlier images: %v", err)
	}
	for _, i := range []int{0, 2} {
		if len(got[i].ImageInputs) != 0 || len(got[i].Images) != 0 {
			t.Fatalf("message %d still carries images: %+v", i, got[i])
		}
		if !strings.Contains(got[i].Content, "cannot read images") {
			t.Fatalf("message %d has no host-owned reason: %q", i, got[i].Content)
		}
	}
	if len(messages[2].ImageInputs) != 1 {
		t.Fatal("canonical history was rewritten")
	}
}

func TestTextOnlyModelStillRejectsTheCurrentUserImageWithATypedReason(t *testing.T) {
	root := t.TempDir()
	c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionPath: filepath.Join(root, "session.jsonl"), ImageRouteConfig: config.Default()})
	messages := []provider.Message{{ID: "now", Role: provider.RoleUser, Content: "look", ImageInputs: []attachment.ImageInput{{Kind: attachment.KindURL, URL: "https://example.invalid/shot.png"}}}}
	_, err := c.ResolveRequestImagesForModel(t.Context(), messages, "declared/text-only", false)
	if !errors.Is(err, imageinput.ErrNoModel) {
		t.Fatalf("err = %v, want imageinput.ErrNoModel", err)
	}
}
