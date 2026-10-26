package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
)

func TestFullRebuildPreservesExternalFolderAccess(t *testing.T) {
	for _, useAlias := range []bool{false, true} {
		name := "native-path"
		if useAlias {
			name = "symlink-alias"
		}
		t.Run(name, func(t *testing.T) { testFullRebuildExternalFolderAccess(t, useAlias) })
	}
}

func testFullRebuildExternalFolderAccess(t *testing.T, useAlias bool) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	writeRuntimeFixture(t, root)
	writeCompactRatio(t, config.UserConfigPath(), .85)
	external := robustTempDir(t)
	writeFile(t, external, "sub/allowed.txt", "external content")
	if useAlias {
		alias := filepath.Join(robustTempDir(t), "external-alias")
		if err := os.Symlink(external, alias); err != nil {
			t.Skipf("symlink fixture unavailable: %v", err)
		}
		external = alias
	}
	outside := robustTempDir(t)
	writeFile(t, outside, "secret.txt", "unregistered content")
	current, err := BuildRuntime(t.Context(), withTestSession(t, Options{WorkspaceRoot: root}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(current.Controller.Close)
	token, _, err := current.Controller.RegisterExternalFolderRef(external)
	if err != nil {
		t.Fatal(err)
	}
	assertExternalFolderAccess(t, current, token, external, outside)
	for _, ratio := range []float64{.80, .75} {
		writeCompactRatio(t, config.UserConfigPath(), ratio)
		next, err := RebuildFrom(t.Context(), current, Options{WorkspaceRoot: root, RuntimeReload: RuntimeReload{ForceFullRebuild: true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(next.Controller.Close)
		if next.ReusedController || next.Controller.CompactRatio() != ratio {
			t.Fatal("expected full settings replacement")
		}
		if err := control.ActivateControllerReplacement(current.Controller, next.Controller); err != nil {
			t.Fatal(err)
		}
		current.Controller.ReleaseResources()
		assertExternalFolderAccess(t, next, token, external, outside)
		current = next
	}
}

func assertExternalFolderAccess(t *testing.T, result *BuildResult, token, external, outside string) {
	t.Helper()
	ctrl := result.Controller
	for _, suffix := range []string{"", "/sub/allowed.txt"} {
		expected, err := filepath.EvalSymlinks(filepath.Join(external, filepath.FromSlash(suffix)))
		if err != nil {
			t.Fatalf("resolve expected external path: %v", err)
		}
		if path, _, ok := ctrl.ExternalFolderRefLocalPath(token + suffix); !ok || path != expected {
			t.Errorf("original token %q lost: path=%q, want=%q, ok=%v", token+suffix, path, expected, ok)
		}
	}
	block, errs := ctrl.ResolveScopedRefs(t.Context(), "read @"+token+"/sub/allowed.txt")
	if len(errs) != 0 || !strings.Contains(block, "external content") {
		t.Errorf("scoped reference lost: %q, %v", block, errs)
	}
	reader, ok := result.Assembly.Registry.Get("read_file")
	if !ok {
		t.Fatal("missing assembled read_file")
	}
	args, _ := json.Marshal(map[string]string{"path": token + "/sub/allowed.txt"})
	content, err := reader.Execute(t.Context(), args)
	if err != nil || !strings.Contains(content, "external content") {
		t.Errorf("tool read root lost: %q, %v", content, err)
	}
	for _, path := range []string{token + "/../secret.txt", "__reasonix_external_folder/unregistered/secret.txt"} {
		if _, _, ok := ctrl.ExternalFolderRefLocalPath(path); ok {
			t.Errorf("reference permits escape %q", path)
		}
		args, _ := json.Marshal(map[string]string{"path": path})
		if content, err := reader.Execute(t.Context(), args); err == nil || strings.Contains(content, "unregistered content") {
			t.Errorf("read tool permits escape %q: %q, %v", path, content, err)
		}
	}
	if _, ok := ctrl.AuthorizedExternalFolderLocalPath(filepath.Join(outside, "secret.txt")); ok {
		t.Error("unregistered root became authorized")
	}
	writer, ok := result.Assembly.Registry.Get("write_file")
	if !ok {
		t.Fatal("missing assembled write_file")
	}
	args, _ = json.Marshal(map[string]string{"path": filepath.Join(external, "sub/allowed.txt"), "content": "unauthorized write"})
	if _, err := writer.Execute(t.Context(), args); err == nil {
		t.Error("read authorization broadened to writes")
	}
}
