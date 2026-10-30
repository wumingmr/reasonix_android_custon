package config

// Where a model's context window comes from, and what happens when nothing
// establishes one. The window is a fact about the model rather than about who
// serves it, but it was only ever recorded per configured entry — so a source
// added by hand resolved to nothing, and a zero window turns compaction off.
// A session then grows until the provider refuses the request.

// DefaultUnknownContextWindow is the conservative working window for a custom
// endpoint that declares no capacity and serves a model nothing known about.
// It is deliberately below the smallest window a modern chat model ships with:
// too small only compacts earlier, while too large makes the provider reject
// the request outright. Users replace it per model with a model_overrides
// entry, or per connection with the provider's own context_window.
const DefaultUnknownContextWindow = 128_000

// ResolvedContextWindow reports the window to run this entry against and
// whether anything established it. Most-specific first: what the configuration
// says for this entry (including its per-model override), then what is known
// about the model itself. Callers that must not confuse "nobody said" with "no
// compaction wanted" need the second return; everything else can use
// EffectiveContextWindow, which also applies the fallback.
func ResolvedContextWindow(e *ProviderEntry) (int, bool) {
	if e == nil {
		return 0, false
	}
	if window := e.declaredContextWindow(); window > 0 {
		return window, true
	}
	if window := builtinModelContextWindow(e); window > 0 {
		return window, true
	}
	return 0, false
}

// EffectiveContextWindow is the window a run must budget against: the declared
// or known one, or the conservative fallback when nothing established it. It is
// never zero, so it never silently disables compaction. A caller that
// deliberately wants compaction off keeps checking the raw
// ProviderEntry.ContextWindow instead.
func EffectiveContextWindow(e *ProviderEntry) int {
	if window, ok := ResolvedContextWindow(e); ok {
		return window
	}
	return DefaultUnknownContextWindow
}

// declaredContextWindow is what the configuration said for this exact model:
// the per-model override first, then the connection-wide value. This ordering
// matches applyModelOverride, which the entry normally went through already;
// checking the override directly keeps the answer right for a raw
// ProviderEntry that never did.
func (e *ProviderEntry) declaredContextWindow() int {
	if e == nil {
		return 0
	}
	if e.Model != "" {
		if ov, ok := e.modelOverrideForModel(e.Model); ok && ov.ContextWindow > 0 {
			return ov.ContextWindow
		}
	}
	if e.ContextWindow > 0 {
		return e.ContextWindow
	}
	return 0
}

// builtinModelContextWindow is what the compiled-in catalog knows about the
// model itself, regardless of which gateway serves it. Only facts the catalog
// actually states count: a model whose window nobody published stays unknown
// instead of inheriting a guess from its family name.
func builtinModelContextWindow(e *ProviderEntry) int {
	if e == nil {
		return 0
	}
	cap, ok := modelReasoningCapabilityForEntry(e)
	if !ok || cap.ContextWindow <= 0 {
		return 0
	}
	return cap.ContextWindow
}
