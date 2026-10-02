package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/transcript"
)

type unlockedRuntimeLogHandler struct {
	slog.Handler
	session *Session
	t       *testing.T
}

func (h unlockedRuntimeLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if !h.session.mu.TryLock() {
		h.t.Error("runtime diagnostic logged while holding the session lock")
	} else {
		h.session.mu.Unlock()
	}
	return h.Handler.Handle(ctx, record)
}

func TestRuntimeFailureDiagnosticIsWrittenToLocalLog(t *testing.T) {
	const secret = "PRIVATE-USER-DATA"
	session := &Session{id: secret + "-session", next: 43}
	for index := range 100 {
		call := fmt.Sprintf("%s-%d", secret, index)
		if index >= 98 {
			call = secret + "-duplicate"
		}
		session.recentMessages = append(session.recentMessages, provider.Message{
			ID: fmt.Sprintf("%s-message-%d", secret, index), Role: provider.RoleTool,
			ToolCallID: call, Content: secret, ReasoningContent: secret,
		})
	}
	path := filepath.Join(t.TempDir(), "service.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	previous := slog.Default()
	slog.SetDefault(slog.New(unlockedRuntimeLogHandler{Handler: slog.NewJSONHandler(file, nil), session: session, t: t}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ref := SessionRef{HostID: "local", SessionID: session.id}
	for range 2 {
		runtime, err := newRuntime(ref, session)
		var initialization *TranscriptInitializationError
		var baseline *transcript.BaselineError
		if runtime != nil || !errors.As(err, &initialization) || !errors.As(err, &baseline) {
			t.Fatalf("lost typed cause: runtime = %v, error = %v", runtime, err)
		}
		if initialization.Classification() != "duplicate_record_identity" {
			t.Fatalf("online classification = %q", initialization.Classification())
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) || len(body) > 4000 {
		t.Fatal("local log contains private data or unbounded diagnostics")
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected one diagnostic per failed attempt, got %d", len(lines))
	}
	for _, line := range lines {
		var entry struct {
			Message    string         `json:"msg"`
			Diagnostic map[string]any `json:"diagnostic"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		diagnostic := entry.Diagnostic
		if entry.Message != "session transcript initialization failed" || diagnostic["session_key"] != fmt.Sprintf("%x", sha256.Sum256([]byte(session.id))) ||
			diagnostic["covered_sequence"] != float64(42) || diagnostic["baseline_message_count"] != float64(96) || diagnostic["baseline_total_message_count"] != float64(100) {
			t.Fatalf("incomplete local diagnostic: %+v", entry)
		}
		baseline, ok := diagnostic["baseline"].(map[string]any)
		if !ok || baseline["code"] != "duplicate_record_identity" || baseline["record_index"] != float64(95) || baseline["previous_record_index"] != float64(94) {
			t.Fatalf("lost bounded-tail conflict details: %+v", baseline)
		}
	}
}
