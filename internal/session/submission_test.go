package session

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestSubmissionMetadataSurvivesReplayForkAndRetraction(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "parent")
	s, err := Open(dir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	receipt := SubmissionReceipt{SessionID: "parent", SubmissionID: "send", Fingerprint: "fingerprint", TurnID: "turn", MessageID: "user"}
	data, _ := json.Marshal(receipt)
	message := provider.Message{ID: "user", Role: provider.RoleUser, Content: "synthetic compatibility fixture"}
	payload, _ := json.Marshal(map[string]any{"message": message})
	commit, err := s.Append(t.Context(), Batch{OperationID: "accept", TurnID: "turn", Events: []Event{
		{Kind: "turn/start"}, {Kind: "submission/accepted", Optional: true, Payload: data},
		{Kind: "message/complete", Payload: payload}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fork(t.Context(), filepath.Join(root, "child"), "child", commit.LastSequence()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(t.Context())
	if got, ok := s.Submission("send"); !ok || got != receipt {
		t.Fatalf("receipt after restart: %+v %v", got, ok)
	}
	model, _ := json.Marshal(s.DeriveMessages())
	if bytes.Contains(model, []byte("submissionId")) || bytes.Contains(model, []byte("fingerprint")) {
		t.Fatal("host metadata entered model messages")
	}
	entries := []PersistentMessage{{MessageID: "user"}}
	attachSubmissionEntries(s.Snapshot().Projection.Submissions, "parent", entries)
	if entries[0].SubmissionID != "send" {
		t.Fatal("recent baseline lost submission identity")
	}
	if rows := s.transcriptRows(s.DeriveMessages()); len(rows) != 1 || rows[0].SubmissionID != "send" {
		t.Fatalf("history: %+v", rows)
	}
	child, err := Open(filepath.Join(root, "child"), "child")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close(t.Context())
	if _, found := child.Submission("send"); found {
		t.Fatal("parent identity leaked into child acceptance scope")
	}
	if len(child.DeriveMessages()) != 1 {
		t.Fatal("fork lost the historical message")
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "retract", Events: []Event{{Kind: "message/retract", Payload: json.RawMessage(`{"messageIds":["user"]}`)}}}); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Submission("send"); !ok || got != receipt {
		t.Fatal("retraction removed acceptance")
	}
}
