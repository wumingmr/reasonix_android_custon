package agent

import (
	"testing"

	"reasonix/internal/provider"
)

func TestOutcomeRunStatePreservesOriginalUncertainty(t *testing.T) {
	for _, tc := range []struct {
		out  toolOutcome
		want provider.ToolRunState
	}{
		{toolOutcome{}, provider.ToolRunNotStarted},
		{toolOutcome{executed: true, output: "write outcome unknown:"}, provider.ToolRunUnknown},
		{toolOutcome{executed: true, errMsg: "context canceled"}, provider.ToolRunUnknown},
		{toolOutcome{executed: true, errMsg: "invalid format"}, provider.ToolRunCompleted},
		{toolOutcome{executed: true, runState: provider.ToolRunCompleted, output: "context canceled"}, provider.ToolRunCompleted},
	} {
		if got := outcomeRunState(tc.out); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
}
