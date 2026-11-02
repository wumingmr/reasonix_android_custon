package agent

import (
	"context"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// A session that grew several times past the window (an earlier fold failed and
// every later turn added more) must be brought back by the automatic trigger,
// not only by a manual /compact. Each attempt folds what one summary request
// can carry and keeps that progress, so repeated attempts converge instead of
// discarding every digest because the view is still over the ceiling.
func TestPressureCompactionFarOverTheWindowConverges(t *testing.T) {
	const window = 20_000
	prov := &denseTokenizerProvider{window: window}
	sess := longASCIISession(120)
	a := New(prov, tool.NewRegistry(), sess, Options{ContextWindow: window, CompactRatio: 0.8}, event.Discard)
	hard := a.hardInputCeiling()
	if est := a.ContextUsedTokens(); est < 3*hard {
		t.Fatalf("fixture estimates %d tokens; want at least three times the ceiling %d", est, hard)
	}

	used := a.ContextUsedTokens()
	for attempt := 1; attempt <= 12; attempt++ {
		err := prepareContext(context.Background(), a, CompactionTriggerPressure)
		now := a.ContextUsedTokens()
		if now >= used {
			t.Fatalf("attempt %d left the view at %d tokens, not below the %d it started from (err=%v)", attempt, now, used, err)
		}
		used = now
		if err == nil {
			if used >= hard {
				t.Fatalf("attempt %d reported success with %d tokens, at or above the ceiling %d", attempt, used, hard)
			}
			return
		}
	}
	t.Fatalf("view still %d tokens (ceiling %d) after 12 attempts", used, hard)
}
