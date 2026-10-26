package boot

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/extension/providerext"
	"reasonix/internal/provider"
)

// Only a threshold is shared, never the mutable config snapshot. Foreground
// executor/planner reads are live; child agents freeze a value at creation.
type liveRuntimeSettings struct{ compactRatioBits atomic.Uint64 }

func newLiveRuntimeSettings(ratio float64) *liveRuntimeSettings {
	s := &liveRuntimeSettings{}
	s.apply(ratio)
	return s
}

func (s *liveRuntimeSettings) compactRatio() float64 {
	return math.Float64frombits(s.compactRatioBits.Load())
}
func (s *liveRuntimeSettings) apply(ratio float64) { s.compactRatioBits.Store(math.Float64bits(ratio)) }

type skillOptionsFactory func(context.Context, int, *provider.Pricing, int, int) agent.Options

func (s *liveRuntimeSettings) skillOptions(factory skillOptionsFactory) skillOptionsFactory {
	return func(ctx context.Context, steps int, price *provider.Pricing, window, depth int) agent.Options {
		opts := factory(ctx, steps, price, window, depth)
		opts.CompactRatio = s.compactRatio()
		return opts
	}
}

// Explicit caller selections can change runtime identity without changing cfg.
// Copy optional effort values rather than retaining mutable caller pointers.
type runtimeSelection struct {
	model, effort string
	hasEffort     bool
}

func runtimeSelectionFrom(opts Options) runtimeSelection {
	s := runtimeSelection{model: opts.Model}
	if opts.EffortOverride != nil {
		s.effort, s.hasEffort = *opts.EffortOverride, true
	}
	return s
}

func validateLiveCompactRatio(ratio float64) error {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return fmt.Errorf("compact ratio must be finite")
	}
	return nil
}

// Compare canonical model identities: a frontend may make the already-selected
// default explicit while saving an unrelated setting.
func runtimeSelectionForConfig(previous *BuildResult, cfg *config.Config, opts Options) (runtimeSelection, error) {
	selection := runtimeSelectionFrom(opts)
	name := opts.Model
	if name == "" {
		if resolved, _, ok := cfg.ResolveNewSessionChatModel(); ok {
			name = resolved
		}
	}
	resolver := opts.ProviderResolver
	if resolver == nil && providerext.PluginRefOwner(name) != "" {
		resolver = previous.ProviderResolver
	}
	_, ref, err := resolveModelEntry(resolver, cfg, name)
	selection.model = ref
	return selection, err
}
