package session

import (
	"context"
	"testing"
)

func TestForkRequiresItsOwnExplicitPermissionPreset(t *testing.T) {
	service, _, parent := newSourceService(t, "preset-parent")
	if _, err := parent.Session().AppendBatch(t.Context(), "parent-preset", []Event{{Kind: "session/permission-preset", Payload: []byte(`{"preset":"danger-full-access"}`)}}); err != nil {
		t.Fatal(err)
	}
	turn := appendCompletedTurn(t, parent, "turn-1", "answer")
	result, err := service.CreateFork(t.Context(), ForkRequest{
		Source: parent.Ref(), TurnID: "turn-1", BoundarySequence: turn.LastSequence(), ChildID: "preset-child", OperationID: "fork-preset",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.Open(t.Context(), result.Child)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Release(context.Background())
	child := binding.Runtime().Session()
	if got := child.Snapshot().Projection.PermissionPreset; got != "danger-full-access" {
		t.Fatalf("fork did not retain the historical parent event: %q", got)
	}
	if got := child.ExplicitPermissionPreset(); got != "" {
		t.Fatalf("child treated inherited preset as an explicit choice: %q", got)
	}
	if _, err := child.AppendBatch(t.Context(), "child-preset", []Event{{Kind: "session/permission-preset", Payload: []byte(`{"preset":"read-only"}`)}}); err != nil {
		t.Fatal(err)
	}
	if got := child.ExplicitPermissionPreset(); got != "read-only" {
		t.Fatalf("child's own choice was not recognized: %q", got)
	}
}
