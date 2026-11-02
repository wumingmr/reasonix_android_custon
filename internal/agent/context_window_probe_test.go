package agent

import (
	"testing"

	"reasonix/internal/provider"
)

// An accepted prompt on its own says nothing about the ceiling: it is a turn
// that happened to be short. Adopting it as the window would compact away
// almost the whole conversation, so the probe stays silent until something was
// actually refused.
func TestProbeIgnoresAcceptedPromptsBeforeAnyRefusal(t *testing.T) {
	a := &Agent{}
	a.noteAcceptedPromptTokens(&provider.Usage{PromptTokens: 1_200})
	if got := a.probedWindow(); got != 0 {
		t.Fatalf("window = %d without a refusal, want no bound", got)
	}
	if got := a.effectiveContextWindow(); got != 0 {
		t.Fatalf("effective window = %d, want the configured zero", got)
	}
}

// The refused size is the first bound: the provider said a prompt that large
// does not fit, so nothing bigger runs. It stands until a fitted measurement
// arrives.
func TestProbeAdoptsTheRefusedSizeAsABound(t *testing.T) {
	a := &Agent{}
	a.noteRejectedPromptTokens(900_000)
	if got := a.probedWindow(); got != 900_000 {
		t.Fatalf("window = %d, want the refused 900000", got)
	}
	// A smaller refusal is a tighter bound; a larger one cannot be, since the
	// smaller prompt already failed.
	a.noteRejectedPromptTokens(700_000)
	a.noteRejectedPromptTokens(800_000)
	if got := a.probedWindow(); got != 700_000 {
		t.Fatalf("window = %d, want the smallest refusal 700000", got)
	}
}

// The measurement that distinguishes a route-wide ceiling from the model's own
// is a prompt that fit after a refusal: that is the only size known to work.
func TestProbePrefersTheLargestAcceptedPromptAfterARefusal(t *testing.T) {
	a := &Agent{}
	a.noteRejectedPromptTokens(900_000)
	a.noteAcceptedPromptTokens(&provider.Usage{PromptTokens: 400_000})
	a.noteAcceptedPromptTokens(&provider.Usage{PromptTokens: 600_000})
	a.noteAcceptedPromptTokens(&provider.Usage{PromptTokens: 500_000})
	if got := a.probedWindow(); got != 600_000 {
		t.Fatalf("window = %d, want the largest accepted 600000", got)
	}
	// An accepted prompt at or above the refusal contradicts it and must not
	// raise the bound: the refusal already proved that size does not fit.
	a.noteAcceptedPromptTokens(&provider.Usage{PromptTokens: 950_000})
	if got := a.probedWindow(); got != 900_000 {
		t.Fatalf("window = %d, an accepted prompt above the refusal raised the bound", got)
	}

	// Smaller accepted prompts keep the largest reachable size: 600000 is known
	// to have fit, while the refusal only says 900000 did not.
	smaller := &Agent{}
	smaller.noteRejectedPromptTokens(900_000)
	smaller.noteAcceptedPromptTokens(&provider.Usage{PromptTokens: 700_000})
	if got := smaller.probedWindow(); got != 700_000 {
		t.Fatalf("window = %d, want the accepted 700000", got)
	}
}

// Estimates are our own arithmetic and never become measurements.
func TestProbeIgnoresEstimatedUsage(t *testing.T) {
	a := &Agent{}
	a.noteRejectedPromptTokens(900_000)
	a.noteAcceptedPromptTokens(&provider.Usage{PromptTokens: 100, Estimated: true})
	if got := a.probedWindow(); got != 900_000 {
		t.Fatalf("window = %d, an estimate was adopted as a measurement", got)
	}
}

// The probe never overrides the window the run was started with: that number
// already came from the configuration, the model catalog or the fallback, and
// the recovery ladder tightens it deliberately. The probe only reports what it
// measured, for surfaces that show where a bound came from.
func TestProbeNeverOverridesTheConfiguredWindow(t *testing.T) {
	a := &Agent{}
	a.contextWindow = 1_048_576
	a.noteRejectedPromptTokens(400_000)
	if got := a.effectiveContextWindow(); got != 1_048_576 {
		t.Fatalf("window = %d, the probe overrode the configured window", got)
	}
	if got := a.probedWindow(); got != 400_000 {
		t.Fatalf("probed window = %d, want the measured 400000", got)
	}
}

// A measurement too small to hold even one fold is reported but never adopted:
// a window nothing can be summarized into is worse than an honest unknown.
func TestAMeasurementBelowTheFoldFloorIsNotAWindow(t *testing.T) {
	if probed := minProbeWindowTokens - 1; probed <= 0 {
		t.Fatal("probe floor must be positive")
	}
	a := &Agent{}
	a.noteRejectedPromptTokens(minProbeWindowTokens - 1)
	if got := a.probedWindow(); got <= 0 {
		t.Fatalf("probed window = %d, the measurement should still be reported", got)
	}
	if got := a.probedWindow(); got >= minProbeWindowTokens {
		t.Fatalf("probed window = %d, a sub-floor measurement must not reach the floor", got)
	}
}
