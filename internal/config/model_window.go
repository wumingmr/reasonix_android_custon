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

// ContextWindowSource names where a window came from. A single number cannot
// say whether compaction fires early because the user asked for a small budget
// or because nothing established the real one, and those two need opposite
// fixes — so the origin travels with the value.
type ContextWindowSource uint8

const (
	// ContextWindowUnestablished is the zero value: nothing said anything.
	ContextWindowUnestablished ContextWindowSource = iota
	// ContextWindowFromModelOverride is a model_overrides entry's context_window.
	ContextWindowFromModelOverride
	// ContextWindowFromProvider is the connection-wide context_window.
	ContextWindowFromProvider
	// ContextWindowFromModelCatalog is the window the built-in catalog records
	// for the model itself, independent of who serves it.
	ContextWindowFromModelCatalog
	// ContextWindowFromFallback is DefaultUnknownContextWindow: a working value
	// no source established.
	ContextWindowFromFallback
)

// String is the stable token used by status surfaces and diagnostics. It is
// not localised, because it also appears in machine-read output.
func (s ContextWindowSource) String() string {
	switch s {
	case ContextWindowFromModelOverride:
		return "model_override"
	case ContextWindowFromProvider:
		return "provider"
	case ContextWindowFromModelCatalog:
		return "model_catalog"
	case ContextWindowFromFallback:
		return "fallback"
	default:
		return "unestablished"
	}
}

// IsEstablished reports whether a real source named this window, as opposed to
// the fallback standing in for one nobody stated.
func (s ContextWindowSource) IsEstablished() bool {
	switch s {
	case ContextWindowFromModelOverride, ContextWindowFromProvider, ContextWindowFromModelCatalog:
		return true
	default:
		return false
	}
}

// ResolvedWindow is a context window together with the source that named it.
type ResolvedWindow struct {
	Tokens int
	Source ContextWindowSource
}

// Established reports whether a real source named this window.
func (w ResolvedWindow) Established() bool { return w.Source.IsEstablished() }

// ResolveContextWindow resolves the window to run this entry against and names
// where it came from. Most-specific first: the per-model override, then the
// connection-wide value, then what the built-in catalog knows about the model
// itself. A caller that must not confuse "nobody said" with "no compaction
// wanted" checks Established; everything else can use EffectiveContextWindow,
// which also applies the fallback.
func ResolveContextWindow(e *ProviderEntry) ResolvedWindow {
	if e == nil {
		return ResolvedWindow{}
	}
	if window, source := e.declaredContextWindow(); window > 0 {
		return ResolvedWindow{Tokens: window, Source: source}
	}
	if window := builtinModelContextWindow(e); window > 0 {
		return ResolvedWindow{Tokens: window, Source: ContextWindowFromModelCatalog}
	}
	return ResolvedWindow{}
}

// ResolvedContextWindow reports the window to run this entry against and
// whether anything established it. It is ResolveContextWindow without the
// origin, kept because most callers only branch on the second return.
func ResolvedContextWindow(e *ProviderEntry) (int, bool) {
	resolved := ResolveContextWindow(e)
	return resolved.Tokens, resolved.Established()
}

// EffectiveContextWindow is the window a run must budget against: the declared
// or known one, or the conservative fallback when nothing established it. It is
// never zero, so it never silently disables compaction. A caller that
// deliberately wants compaction off keeps checking the raw
// ProviderEntry.ContextWindow instead.
func EffectiveContextWindow(e *ProviderEntry) int {
	return EffectiveResolvedWindow(e).Tokens
}

// EffectiveResolvedWindow is EffectiveContextWindow keeping the origin, which
// surfaces use to explain the number instead of only printing it.
func EffectiveResolvedWindow(e *ProviderEntry) ResolvedWindow {
	resolved := ResolveContextWindow(e)
	if resolved.Tokens > 0 {
		return resolved
	}
	return ResolvedWindow{Tokens: DefaultUnknownContextWindow, Source: ContextWindowFromFallback}
}

// declaredContextWindow is what the configuration said for this exact model:
// the per-model override first, then the connection-wide value. This ordering
// matches applyModelOverride, which the entry normally went through already;
// checking the override directly keeps the answer right for a raw
// ProviderEntry that never did.
func (e *ProviderEntry) declaredContextWindow() (int, ContextWindowSource) {
	if e == nil {
		return 0, ContextWindowUnestablished
	}
	if e.Model != "" {
		if ov, ok := e.modelOverrideForModel(e.Model); ok && ov.ContextWindow > 0 {
			return ov.ContextWindow, ContextWindowFromModelOverride
		}
	}
	if e.ContextWindow > 0 {
		// applyModelOverride already folded the override into ContextWindow, so an
		// entry that went through resolution reports the same number under this
		// branch. The override check above runs first for that reason.
		return e.ContextWindow, ContextWindowFromProvider
	}
	return 0, ContextWindowUnestablished
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
