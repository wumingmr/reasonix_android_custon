package agent

import (
	"sync/atomic"

	"reasonix/internal/provider"
)

// contextWindowProbe is what an endpoint that declares no usable window still
// tells us about it, measured from requests this agent actually sent: the
// smallest prompt it refused as too long, and the largest prompt it accepted
// afterwards. A refusal states a ceiling nobody can exceed; an accepted prompt
// states a size that did reach the model. Together they bound the real window
// instead of guessing at one.
//
// The Agent's learnedContextBudget already adopts the window a refusal names.
// This probe exists because that number is only the provider's report: relays
// commonly answer with the ceiling of the route rather than of the model, and
// a window adopted from it alone can be as wrong as no window at all. An
// accepted measurement taken after the refusal is the only observation that
// distinguishes the two.
//
// The zero value is ready to use, because an Agent must never carry a window
// nobody established.
type contextWindowProbe struct {
	rejected promptTokenExtreme
	// accepted is only written once a refusal established what does not fit.
	// An accepted prompt before that says nothing about the ceiling — it is a
	// turn that happened to be short — and adopting it as a window would
	// compact away almost the whole conversation.
	accepted promptTokenExtreme
	// refused marks that a refusal has happened, so the accepted side stays
	// dormant until then.
	refused atomic.Bool
}

// promptTokenExtreme tracks one end of a sequence of token counts. Zero is the
// empty state, so a caller reads get() == 0 as "nothing observed".
type promptTokenExtreme struct {
	value atomic.Int64
}

// noteMin records tokens when it is smaller than what is already known.
func (t *promptTokenExtreme) noteMin(tokens int) {
	if t == nil || tokens <= 0 {
		return
	}
	next := int64(tokens)
	for {
		current := t.value.Load()
		if current != 0 && next >= current {
			return
		}
		if t.value.CompareAndSwap(current, next) {
			return
		}
	}
}

// noteMax records tokens when it is larger than what is already known.
func (t *promptTokenExtreme) noteMax(tokens int) {
	if t == nil || tokens <= 0 {
		return
	}
	next := int64(tokens)
	for {
		current := t.value.Load()
		if current != 0 && next <= current {
			return
		}
		if t.value.CompareAndSwap(current, next) {
			return
		}
	}
}

func (t *promptTokenExtreme) get() int {
	if t == nil {
		return 0
	}
	return int(t.value.Load())
}

// noteRejectedPromptTokens records a prompt the provider refused as too long.
func (a *Agent) noteRejectedPromptTokens(tokens int) {
	if a == nil {
		return
	}
	a.windowProbe.rejected.noteMin(tokens)
	a.windowProbe.refused.Store(true)
}

// noteAcceptedPromptTokens records a provider-counted prompt size that reached
// the model. It applies the same trust rule as prompt calibration: an estimate
// is our own arithmetic and may not become a measurement.
func (a *Agent) noteAcceptedPromptTokens(usage *provider.Usage) {
	if a == nil || usage == nil || usage.Estimated || !a.windowProbe.refused.Load() {
		return
	}
	a.windowProbe.accepted.noteMax(usage.LatestPromptTokens())
}

// probedWindow is the window the probe establishes, or zero while it has
// established nothing. Zero means "no evidence", which leaves the configured
// window — including the conservative fallback — in charge.
//
// The accepted bound wins when it exists: it is the only size known to have
// fit. The refused size stands in until such a measurement arrives, because a
// prompt the provider just rejected is a bound it cannot exceed either way.
func (a *Agent) probedWindow() int {
	if a == nil || !a.windowProbe.refused.Load() {
		return 0
	}
	rejected := a.windowProbe.rejected.get()
	if rejected <= 0 {
		return 0
	}
	if accepted := a.windowProbe.accepted.get(); accepted > 0 && accepted < rejected {
		return accepted
	}
	return rejected
}

// minProbeWindowTokens is the smallest window a measurement is allowed to
// establish. Below it, no fold can be summarized into something the window
// holds, so adopting the number would leave every later turn unserviceable
// rather than merely conservative. It matches the floor the summary planner
// works with, expressed once here so the two cannot drift apart.
const minProbeWindowTokens = 8 * 1024
