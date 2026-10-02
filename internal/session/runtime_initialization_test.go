package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/provider"
)

// Both messages have valid canonical identities, but their display records
// collide. This reproduces a fallible transcript constructor after a writer
// has already been acquired.
func appendConflictingTranscript(t *testing.T, session *Session) {
	t.Helper()
	var events []Event
	for _, id := range []string{"result-one", "result-two"} {
		payload, err := json.Marshal(map[string]any{"message": provider.Message{
			ID: id, Role: provider.RoleTool, ToolCallID: "reused-call", Content: id,
		}})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, Event{Kind: "message/complete", Payload: payload})
	}
	if _, err := session.AppendBatch(t.Context(), "conflicting-transcript", events); err != nil {
		t.Fatal(err)
	}
}

type conflictingTranscriptPersistence struct {
	SessionPersistence
	t *testing.T
}

func (p conflictingTranscriptPersistence) Create(options CreateOptions) (*Session, error) {
	session, err := p.SessionPersistence.Create(options)
	if err == nil {
		appendConflictingTranscript(p.t, session)
	}
	return session, err
}

func TestRuntimeInitializationFailureReleasesWriterAndAllowsRetry(t *testing.T) {
	for _, entry := range []string{"create", "open"} {
		t.Run(entry, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			persistence := NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4"))
			var servicePersistence SessionPersistence = persistence
			if entry == "create" {
				servicePersistence = conflictingTranscriptPersistence{SessionPersistence: persistence, t: t}
			} else {
				session, err := persistence.Create(CreateOptions{SessionID: "conflict"})
				if err != nil {
					t.Fatal(err)
				}
				appendConflictingTranscript(t, session)
				if err := session.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			service, err := NewService("local", servicePersistence)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := service.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			ref := SessionRef{HostID: "local", SessionID: "conflict"}
			if entry == "create" {
				prepared, err := service.PrepareCreate(ctx, CreateOptions{SessionID: ref.SessionID})
				if prepared != nil || err == nil || !strings.Contains(err.Error(), `duplicate transcript record "tool:reused-call"`) {
					t.Fatalf("prepare = %v, error = %v", prepared, err)
				}
			}
			// A retry must return the original validation error, not wait on a
			// stranded preparation or fail on a leaked writer lease.
			for range 2 {
				binding, err := service.Open(ctx, ref)
				if binding != nil || err == nil || !strings.Contains(err.Error(), `initialize transcript for "conflict": duplicate transcript record "tool:reused-call"`) {
					t.Fatalf("open = %v, error = %v", binding, err)
				}
				if _, ok := service.Runtime(ref); ok {
					t.Fatal("failed initialization published a runtime")
				}
			}
			// Reopen through persistence to prove cleanup retained both original
			// messages and released the writer for a later repair.
			repair, err := persistence.Open(ref.SessionID, ReadWrite)
			if err != nil {
				t.Fatal(err)
			}
			messages := repair.ExecutionSnapshot().Projection.ModelMessages
			if len(messages) != 2 || messages[0].ID != "result-one" || messages[1].ID != "result-two" {
				t.Fatalf("failed initialization changed stored history: %+v", messages)
			}
			payload, err := json.Marshal(map[string]any{"message": provider.Message{
				ID: "result-two", Role: provider.RoleTool, ToolCallID: "distinct-call", Content: "result-two",
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repair.AppendBatch(ctx, "repair", []Event{{Kind: "message/upsert", Payload: payload}}); err != nil {
				t.Fatal(err)
			}
			if err := repair.Close(ctx); err != nil {
				t.Fatal(err)
			}
			binding, err := service.Open(ctx, ref)
			if err != nil {
				t.Fatalf("open after repair: %v", err)
			}
			defer binding.Release(context.Background())
			if binding.Runtime().Transcript() == nil {
				t.Fatal("successful runtime has no transcript")
			}
		})
	}
}
