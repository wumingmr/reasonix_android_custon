package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
	"reasonix/internal/provider"
)

func TestRejectedToolEvidenceSurvivesWireResetAndColdRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions-v4", "replay")
	s, err := CreateWithOptions(dir, "replay", OpenOptions{ExternalHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	messages := []provider.Message{
		{ID: "proposal", Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call", Name: "use_capability", Arguments: `{"body":"["text"]"}`}}},
		{ID: "receipt", Role: provider.RoleTool, Name: "use_capability", ToolCallID: "call", ToolRunState: provider.ToolRunNotStarted, Content: "validation failed; not executed"},
	}
	appendReplayEvent(t, s, "proposal", "message/complete", map[string]any{"message": messages[0]})
	appendReplayEvent(t, s, "receipt", "message/complete", map[string]any{"message": messages[1]})
	appendReplayEvent(t, s, "wire-reset", "model/context-replace", map[string]any{"messages": provider.ModelMessages(messages), "reason": "interrupted-turn-cleanup"})
	assertReplayEvidence(t, s)
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []bool{false, true} {
		if stale {
			invalidateReplayCheckpointVersion(t, dir)
		}
		var stats RecoveryOpenStats
		s, err = OpenWithOptions(dir, "replay", OpenOptions{ExternalHistory: true, ObserveRecovery: func(got RecoveryOpenStats) { stats = got }})
		if err != nil {
			t.Fatal(err)
		}
		if stats.UsedCheckpoint == stale {
			t.Fatalf("checkpoint used=%v, stale=%v", stats.UsedCheckpoint, stale)
		}
		assertReplayEvidence(t, s)
		if err := s.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func appendReplayEvent(t *testing.T, s *Session, op, kind string, body any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: op, Events: []Event{{Kind: kind, Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
}

func assertReplayEvidence(t *testing.T, s *Session) {
	t.Helper()
	view := s.ExecutionSnapshot().Projection
	if len(view.Messages) != 0 || view.ModelMessages[1].ToolRunState != provider.ToolRunNotStarted {
		t.Fatal("execution evidence lost or UI history materialized")
	}
	if err := provider.ValidateModelTranscript(provider.RepairRejectedArguments(view.ModelMessages)); err != nil {
		t.Fatal(err)
	}
	view.ModelMessages[0].ToolCalls[0].Arguments = "mutated"
	delete(view.RejectedToolResults, "receipt")
	canonical := s.Snapshot().Projection
	if canonical.Messages[0].ToolCalls[0].Arguments != `{"body":"["text"]"}` || canonical.ModelMessages[1].ToolRunState != "" {
		t.Fatal("request repair mutated stored history or wire projection")
	}
	if s.ExecutionSnapshot().Projection.ModelMessages[1].ToolRunState != provider.ToolRunNotStarted {
		t.Fatal("snapshot mutation leaked into execution evidence")
	}
}

func invalidateReplayCheckpointVersion(t *testing.T, dir string) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(recoveryCacheDir(dir), recoveryDBName), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(recoveryCheckpointBucket)
		for _, key := range [][]byte{recoveryCurrentKey, recoveryPreviousKey} {
			raw := bucket.Get(key)
			if len(raw) == 0 {
				continue
			}
			var checkpoint recoveryCheckpoint
			if err := decodeRecoveryValue(raw, &checkpoint); err != nil {
				return err
			}
			checkpoint.ProjectionVersion = recoveryProjectionVersion - 1
			checkpoint.Projection.RejectedToolResults = nil
			raw, err := encodeRecoveryValue(checkpoint)
			if err != nil {
				return err
			}
			if err := bucket.Put(key, raw); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedToolEvidenceNeverTransfersAcrossResultIdentity(t *testing.T) {
	p := Projection{}
	result := provider.Message{ID: "one", Role: provider.RoleTool, ToolCallID: "reused", Name: "write", ToolRunState: provider.ToolRunNotStarted}
	noteRejectedToolResult(&p, result)
	for _, next := range []provider.Message{
		{ID: "two", Role: provider.RoleTool, ToolCallID: "reused", Name: "write"},
		{ID: "one", Role: provider.RoleTool, ToolCallID: "reused", Name: "other"},
		{ID: "one", Role: provider.RoleTool, ToolCallID: "reused", Name: "write", ToolRunState: provider.ToolRunCompleted},
	} {
		p.ModelMessages = []provider.Message{next}
		restoreRejectedToolResults(&p)
		if p.ModelMessages[0].ToolRunState != next.ToolRunState {
			t.Fatal("refusal transferred to another result")
		}
	}
	result.ToolRunState = provider.ToolRunUnknown
	noteRejectedToolResult(&p, result)
	if len(p.RejectedToolResults) != 0 {
		t.Fatal("updated state retained obsolete evidence")
	}
}

func TestRejectedToolEvidenceFollowsCanonicalRemoval(t *testing.T) {
	for _, kind := range []string{"message/retract", "history/replace", "message/upsert"} {
		t.Run(kind, func(t *testing.T) {
			p := Projection{}
			m := provider.Message{ID: "receipt", Role: provider.RoleTool, ToolCallID: "call", Name: "read", ToolRunState: provider.ToolRunNotStarted}
			noteRejectedToolResult(&p, m)
			body := map[string]any{"messageIds": []string{m.ID}}
			switch kind {
			case "history/replace":
				body = map[string]any{"messages": []provider.Message{}}
			case "message/upsert":
				m.ToolRunState = provider.ToolRunCompleted
				body = map[string]any{"message": m}
			}
			payload, _ := json.Marshal(body)
			applyTranscriptMetadata(&p, Commit{}, Event{Kind: kind, Payload: payload})
			if len(p.RejectedToolResults) != 0 {
				t.Fatal("canonical removal left refusal authority behind")
			}
		})
	}
}
