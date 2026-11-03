package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

func TestBuildUnknownImageAttachmentReachesProvider(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	seen := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		attached := false
		for _, message := range request.Messages {
			if strings.Contains(string(message.Content), `"type":"image_url"`) {
				attached = true
			}
		}
		seen <- attached
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"synthetic answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.DefaultModel = "relay/codebuddy/deepseek-v4.1-flash"
	cfg.Providers = []config.ProviderEntry{{Name: "relay", Kind: "openai", BaseURL: srv.URL, Model: "codebuddy/deepseek-v4.1-flash", APIKeyEnv: "UNKNOWN_IMAGE_TEST_KEY"}}
	t.Setenv("UNKNOWN_IMAGE_TEST_KEY", "test-key")
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(root, ".reasonix", "attachments", "probe.png")
	if err := os.MkdirAll(filepath.Dir(imagePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	approveWorkspace(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctrl, err := Build(ctx, Options{WorkspaceRoot: root, Sink: event.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(ctx, "describe @.reasonix/attachments/probe.png"); err != nil {
		t.Fatal(err)
	}
	select {
	case attached := <-seen:
		if !attached {
			t.Fatal("model request lost explicit image attachment")
		}
	default:
		t.Fatal("provider request was not made")
	}
}

func TestLocalResolverKeepsUnknownImagesAvailableWithoutFallback(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{{Name: "relay", Kind: "openai", BaseURL: "http://127.0.0.1:8790/v1", Model: "codebuddy/deepseek-v4.1-flash"}}
	resolver := NewLocalProviderResolverWithCapabilities(cfg, netclient.ProxySpec{}, config.NewTransientModelCapabilityResolver())
	p, err := resolver.Resolve(provider.Selection{Ref: "relay/codebuddy/deepseek-v4.1-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if p.(provider.ModelInfoProvider).ModelInfo().InputModalities != nil {
		t.Fatal("unknown gateway model was falsely declared text-only")
	}
	if !runtimeImageEnabled(p, true) {
		t.Fatal("unknown image capability disabled current-model attachments")
	}
	if runtimeImageEnabled(p, false) {
		t.Fatal("unknown image capability bypassed configured fallback")
	}
}
