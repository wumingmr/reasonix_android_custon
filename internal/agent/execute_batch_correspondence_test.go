package agent

import (
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestBatchToolResultCorrespondencePreservesOrderAndCallIDs(t *testing.T) {
	calls := []provider.ToolCall{{ID: "first", Name: "pwsh"}, {ID: "second", Name: "pwsh"}}
	results := []provider.Message{
		{Role: provider.RoleTool, ToolCallID: "first", Name: "pwsh", Content: "same"},
		{Role: provider.RoleTool, ToolCallID: "second", Name: "pwsh", Content: "same"},
	}
	if err := validateBatchToolResultCorrespondence(calls, results, []bool{true, true}); err != nil {
		t.Fatal(err)
	}
	results[1].ToolCallID = "first"
	if err := validateBatchToolResultCorrespondence(calls, results, []bool{true, true}); err == nil || !strings.Contains(err.Error(), "second") {
		t.Fatalf("expected mismatched call id failure, got %v", err)
	}
}
