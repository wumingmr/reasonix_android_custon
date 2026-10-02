package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func TestLegacyMigrationTranscriptFailurePreservesSourceAndContinues(t *testing.T) {
	for _, mode := range []string{"legacy", "paired"} {
		t.Run(mode, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			oldVersion, oldEndpoint := version, crashEndpoint
			version = "v9.9.9"
			t.Cleanup(func() { version, crashEndpoint = oldVersion, oldEndpoint })
			var uploaded []crashReport
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var report crashReport
				if err := json.NewDecoder(request.Body).Decode(&report); err != nil {
					t.Error(err)
				} else {
					uploaded = append(uploaded, report)
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()
			crashEndpoint = server.URL
			root := t.TempDir()
			logPath := filepath.Join(root, "service.log")
			logFile, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = logFile.Close() })
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(logFile, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })
			legacyDir := filepath.Join(root, "sessions")
			badPath := filepath.Join(legacyDir, "a-conflicting.jsonl")
			bad := agent.NewSession("system")
			bad.Add(provider.Message{ID: "user", Role: provider.RoleUser, Content: "retained question"})
			bad.Add(provider.Message{ID: "result-one", Role: provider.RoleTool, ToolCallID: "reused-call", Content: "first result"})
			bad.Add(provider.Message{ID: "result-two", Role: provider.RoleTool, ToolCallID: "reused-call", Content: "second result"})
			if err := bad.Save(badPath); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(badPath)
			if err != nil {
				t.Fatal(err)
			}
			goodPath := filepath.Join(legacyDir, "b-healthy.jsonl")
			good := agent.NewSession("system")
			good.Add(provider.Message{ID: "healthy-user", Role: provider.RoleUser, Content: "healthy conversation"})
			if err := good.Save(goodPath); err != nil {
				t.Fatal(err)
			}
			source := desktopMigrationSource{root: legacyDir, scope: "global"}
			if mode == "paired" {
				source.pairedRoot = filepath.Join(root, "sessions-v4")
			}
			var targetID string
			// A fresh app exercises the next startup, including persisted
			// migration bookkeeping, without touching the original source.
			for attempt := range 2 {
				app := NewApp()
				app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
				app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
				t.Cleanup(app.closeSessionServices)
				err := app.migrateLegacyDirectory(t.Context(), source)
				if err == nil || !strings.Contains(err.Error(), `duplicate transcript record "tool:reused-call"`) {
					t.Fatalf("migration error = %v", err)
				}
				ledger, err := readDesktopMigrationLedger()
				if err != nil {
					t.Fatal(err)
				}
				failed := ledger.Records[desktopLegacyMigrationKey(badPath)]
				if failed.Status != "failed" || failed.ErrorCode != "legacy_import" {
					t.Fatalf("failed migration was not recorded: %+v", failed)
				}
				completed := ledger.Records[desktopLegacyMigrationKey(goodPath)]
				if completed.Status != "completed" || completed.TargetSessionID == "" {
					t.Fatalf("healthy migration did not continue: %+v", completed)
				}
				if targetID != "" && targetID != completed.TargetSessionID {
					t.Fatal("restart duplicated the healthy session")
				}
				targetID = completed.TargetSessionID
				state, err := app.desktopSessions.workspaceState.Load(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				ids := state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs
				if len(ids) != 1 || ids[0] != targetID {
					t.Fatalf("workspace sessions = %v", ids)
				}
				current, err := os.ReadFile(badPath)
				if err != nil || !bytes.Equal(original, current) {
					t.Fatalf("failed migration changed the source: %v", err)
				}
				logBody, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				for _, private := range []string{root, "retained question", "first result", "second result", "reused-call"} {
					if bytes.Contains(logBody, []byte(private)) {
						t.Fatalf("migration diagnostic exposed private value %q", private)
					}
				}
				migrationLogs, runtimeLogs := 0, 0
				var sessionKey string
				for line := range bytes.SplitSeq(bytes.TrimSpace(logBody), []byte("\n")) {
					var entry struct {
						Message    string `json:"msg"`
						SourceKey  string `json:"source_key"`
						Diagnostic struct {
							SessionKey string                `json:"session_key"`
							Baseline   struct{ Code string } `json:"baseline"`
						} `json:"diagnostic"`
					}
					if err := json.Unmarshal(line, &entry); err != nil {
						t.Fatal(err)
					}
					switch entry.Message {
					case "session transcript initialization failed":
						runtimeLogs++
						sessionKey = entry.Diagnostic.SessionKey
					case "desktop session migration transcript initialization failed":
						migrationLogs++
						if entry.SourceKey != failed.SourceKey || sessionKey == "" || entry.Diagnostic.SessionKey != sessionKey || entry.Diagnostic.Baseline.Code != "duplicate_record_identity" {
							t.Fatalf("migration log cannot be correlated with runtime and ledger: %+v", entry)
						}
					}
				}
				if migrationLogs != attempt+1 || runtimeLogs != attempt+1 {
					t.Fatalf("missing diagnostics after restart: migration=%d runtime=%d", migrationLogs, runtimeLogs)
				}
				pending := pendingCrashQueuePaths()
				if len(pending) != attempt+1 {
					t.Fatalf("pending online diagnostics = %d, want %d", len(pending), attempt+1)
				}
				reportBody, err := os.ReadFile(pending[len(pending)-1])
				if err != nil {
					t.Fatal(err)
				}
				var report crashReport
				if err := json.Unmarshal(reportBody, &report); err != nil {
					t.Fatal(err)
				}
				if report.Kind != "exception" || report.Source != "desktop.session_migration" ||
					report.Label != "transcript.initialization" || report.ErrorType != "TranscriptInitializationError" ||
					!strings.Contains(report.FingerprintHint, "duplicate_record_identity") || report.InstallID != "" {
					t.Fatalf("online diagnostic is incomplete: %+v", report)
				}
				for _, private := range []string{root, failed.SourceKey, sessionKey, "retained question", "first result", "second result", "reused-call"} {
					if bytes.Contains(reportBody, []byte(private)) {
						t.Fatalf("online diagnostic exposed local value %q", private)
					}
				}
				app.closeSessionServices()
			}
			NewApp().flushPendingCrash()
			if len(uploaded) != 2 || uploaded[0].Source != "desktop.session_migration" || uploaded[1].Source != "desktop.session_migration" || len(pendingCrashPaths()) != 0 {
				t.Fatalf("diagnostic delivery did not preserve both startup failures: uploaded=%+v pending=%v", uploaded, pendingCrashPaths())
			}
			if uploaded[0].EventID == "" || uploaded[0].EventID == uploaded[1].EventID {
				t.Fatalf("separate startup failures reused an event identity: uploaded=%+v", uploaded)
			}
			if uploaded[0].DedupKey == "" || uploaded[0].DedupKey != uploaded[1].DedupKey {
				t.Fatalf("equivalent startup failures lost their correlation key: uploaded=%+v", uploaded)
			}
		})
	}
}
