package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestForkCreatesIndependentSessionAtCompletedTurnBoundary(t *testing.T) {
	root := t.TempDir()
	parentDir := filepath.Join(root, "parent")
	parent, err := Open(parentDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	message, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "m1", Role: provider.RoleUser, Content: "one"}})
	first, err := parent.Append(t.Context(), Batch{OperationID: "turn-1", TurnID: "t1", Events: []Event{
		{Kind: "turn/start"}, {Kind: "message/complete", Payload: message}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	secondMessage, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "m2", Role: provider.RoleUser, Content: "two"}})
	if _, err := parent.Append(t.Context(), Batch{OperationID: "turn-2", TurnID: "t2", Events: []Event{
		{Kind: "turn/start"}, {Kind: "message/complete", Payload: secondMessage}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(root, "child")
	if _, err := parent.Fork(t.Context(), childDir, "child", first.LastSequence()); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	child, err := Open(childDir, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close(context.Background())
	projection := child.Snapshot().Projection
	if len(projection.Messages) != 1 || projection.Messages[0].ID != "m1" {
		t.Fatalf("child projection = %+v", projection)
	}
	if _, err := child.Append(t.Context(), Batch{OperationID: "child-turn", TurnID: "ct", Events: []Event{{Kind: "turn/start"}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	if child.Snapshot().EventSequence == parent.Snapshot().EventSequence {
		t.Fatal("child append mutated parent sequence")
	}
	if _, err := Replay(parentDir, nil); err != nil {
		t.Fatal(err)
	}
}

func TestForkRejectsMidTurnCut(t *testing.T) {
	root := t.TempDir()
	parent, err := Open(filepath.Join(root, "parent"), "parent")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close(context.Background())
	if _, err := parent.Append(t.Context(), Batch{OperationID: "open", TurnID: "t1", Events: []Event{{Kind: "turn/start"}, {Kind: "step/start", Payload: json.RawMessage(`{"id":"step-1"}`)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Fork(t.Context(), filepath.Join(root, "child"), "child", 1); err == nil {
		t.Fatal("mid-turn fork succeeded")
	}
}
