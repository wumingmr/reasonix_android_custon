package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestCheckpointRetentionSurvivesUnrelatedUserConfigSave(t *testing.T) {
	isolateUserConfigHome(t)
	path := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[checkpoints]\nretain_turns = 200\nblob_quota_bytes = 2147483648\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := LoadForEditWithoutCredentials(path)
	want := cfg.Checkpoints
	if want.RetainTurns != 200 {
		t.Fatalf("load = %+v", want)
	}
	cfg.UI.Theme = "dark"
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	got := LoadForEditWithoutCredentials(path).Checkpoints
	if got != want {
		t.Fatalf("save unrelated theme lost retention: got %+v want %+v", got, want)
	}
}

func TestCheckpointRetentionProjectSavePreservesInheritance(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("[checkpoints]\nretain_turns = 200\nblob_quota_bytes = 2147483648\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "reasonix.toml")
	cfg := Default()
	cfg.Checkpoints.RetainTurns = 300
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	// Exercise both new-file rendering and the existing-file delta writer.
	cfg = LoadForEditWithoutCredentials(path)
	cfg.Checkpoints.RetainTurns = 400
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "blob_quota_bytes") {
		t.Fatal("project save materialized an unset quota")
	}
	effective, err := LoadForRootWithoutCredentialsReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	want := CheckpointsConfig{RetainTurns: 400, BlobQuotaBytes: 2147483648}
	if effective.Checkpoints != want {
		t.Fatalf("effective retention = %+v, want %+v", effective.Checkpoints, want)
	}
}

func TestCheckpointRetentionRenderingKeepsUnsetKeysAbsent(t *testing.T) {
	isolateUserConfigHome(t)
	for _, retention := range []CheckpointsConfig{{}, {RetainTurns: 200}, {BlobQuotaBytes: 2147483648}, {RetainTurns: -1, BlobQuotaBytes: -1}} {
		cfg := Default()
		cfg.Checkpoints = retention
		for _, body := range []string{RenderTOML(cfg), RenderTOMLForScope(cfg, RenderScopeUser), RenderTOMLForScope(cfg, RenderScopeProject), RenderTOMLProjectDelta(cfg)} {
			var got Config
			meta, err := toml.Decode(body, &got)
			if err != nil {
				t.Fatal(err)
			}
			if got.Checkpoints != retention {
				t.Fatalf("round trip = %+v, want %+v", got.Checkpoints, retention)
			}
			if meta.IsDefined("checkpoints", "retain_turns") != (retention.RetainTurns != 0) || meta.IsDefined("checkpoints", "blob_quota_bytes") != (retention.BlobQuotaBytes != 0) {
				t.Fatalf("render changed key presence for %+v", retention)
			}
		}
	}
}

func TestCheckpointRetentionRenderScopes(t *testing.T) {
	isolateUserConfigHome(t)
	cfg := Default()
	cfg.Checkpoints = CheckpointsConfig{RetainTurns: 200, BlobQuotaBytes: 2147483648}
	for _, scope := range []RenderScope{RenderScopeFull, RenderScopeUser, RenderScopeProject} {
		var got Config
		if _, err := toml.Decode(RenderTOMLForScope(cfg, scope), &got); err != nil {
			t.Fatal(err)
		}
		if got.Checkpoints != cfg.Checkpoints {
			t.Errorf("scope %v lost retention: %+v", scope, got.Checkpoints)
		}
	}
	var got Config
	if _, err := toml.Decode(RenderTOMLProjectDelta(cfg), &got); err != nil {
		t.Fatal(err)
	}
	if got.Checkpoints != cfg.Checkpoints {
		t.Errorf("project delta lost retention: %+v", got.Checkpoints)
	}
}
