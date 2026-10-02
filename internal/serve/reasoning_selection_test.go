package serve

import (
	"context"
	"os"
	"strings"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
)

func TestAutoClearsSessionEffortWithUnknownMetadata(t *testing.T) {
	writeServeModelConfig(t)
	path := config.UserConfigPath()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.ReplaceAll(string(body), "supported_efforts = [\"low\", \"high\"]", ""))
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc, ModelRef: "alternate/shared-chat", SessionDir: t.TempDir()})
	s := New(ctrl, bc, config.ServeConfig{})
	defer s.Close()
	high := "high"
	s.buildOptions.EffortOverride = &high
	builds := 0
	s.buildControllerWithOptions = func(_ context.Context, ref string, opts boot.Options) (*control.Controller, error) {
		builds++
		if opts.EffortOverride == nil || *opts.EffortOverride != "" || opts.EffortModel != ref {
			t.Fatalf("auto lost its override before boot: %+v", opts.EffortOverride)
		}
		cfg := config.LoadForEdit(path)
		if err := boot.ValidateReasoningSnapshot(cfg, opts); err != nil {
			return nil, err
		}
		return control.New(control.Options{Sink: opts.Sink, ModelRef: ref, SessionDir: t.TempDir()}), nil
	}
	if err := s.switchEffort(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	if builds != 1 || s.buildOptions.EffortOverride == nil || *s.buildOptions.EffortOverride != "" {
		t.Fatal("auto did not commit")
	}
}
