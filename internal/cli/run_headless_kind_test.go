package cli

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
)

// A one-shot run persists a resumable store; its manifest must say it is one,
// or every conversation list treats it as a conversation the user started.
func TestRunPrintRecordsHeadlessRunKind(t *testing.T) {
	home := isolateCLIConfigHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()
	t.Setenv("REASONIX_TEST_RUN_KEY", "test-key")
	path := config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "default_model = \"fake/model-a\"\n\n[[providers]]\nname = \"fake\"\nkind = \"openai\"\nbase_url = \"" + srv.URL + "/v1\"\nmodels = [\"model-a\"]\ndefault = \"model-a\"\napi_key_env = \"REASONIX_TEST_RUN_KEY\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var rc int
	_ = captureStdout(t, func() {
		_ = captureStderr(t, func() {
			rc = runAgent([]string{"-p", "list the tools"}, "dev")
		})
	})
	if rc != 0 {
		t.Fatalf("run -p rc = %d, want 0", rc)
	}
	closeTestSessionServices()

	var kinds []string
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "manifest.json" || filepath.Base(filepath.Dir(filepath.Dir(p))) != "sessions-v4" {
			return nil
		}
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var manifest struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, manifest.Kind)
		return nil
	})
	if len(kinds) != 1 || kinds[0] != "headless-run" {
		t.Fatalf("run -p store kinds = %q, want one headless-run store", kinds)
	}
}
