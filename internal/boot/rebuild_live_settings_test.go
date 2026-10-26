package boot

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
)

func liveSettingsFixture(t *testing.T) (*BuildResult, *config.Config, string) {
	t.Helper()
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	writeRuntimeFixture(t, root)
	writeCompactRatio(t, config.UserConfigPath(), .85)
	cfg, err := config.LoadModelRuntimeSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	old, err := BuildRuntime(t.Context(), withTestSession(t, Options{WorkspaceRoot: root, ConfigSnapshot: cfg}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Controller.Close)
	return old, cfg, root
}

func TestRebuildFromUnrelatedSettingsReusesController(t *testing.T) {
	old, cfg, root := liveSettingsFixture(t)
	cfg.UI.Theme = "light"
	cfg.Desktop.Theme = "dark"
	cfg.Desktop.LayoutStyle = "creation"
	cfg.Notifications.Enabled = !cfg.Notifications.Enabled
	next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root, ConfigSnapshot: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Controller.Close)
	if !next.ReusedController || next.Controller != old.Controller || next.liveSettings != old.liveSettings {
		t.Fatal("frontend preferences rebuilt the runtime")
	}
	if next.Snapshot.CacheHash() != old.Snapshot.CacheHash() || next.Plan.PrefixChanged {
		t.Fatal("frontend preferences changed provider cache prefix")
	}
}

func TestLiveCompactRatioFailedNarrowRebuildIsAtomic(t *testing.T) {
	old, cfg, root := liveSettingsFixture(t)
	cfg.Agent.CompactRatio = .8
	generation := old.Controller.RuntimeGeneration()
	history := old.Controller.History()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	next, err := RebuildFrom(ctx, old, Options{WorkspaceRoot: root, ConfigSnapshot: cfg})
	if !errors.Is(err, context.Canceled) || next != nil {
		t.Fatalf("cancelled narrow rebuild=%v,%v", next, err)
	}
	if old.Controller.CompactRatio() != .85 || old.Controller.RuntimeGeneration() != generation || !reflect.DeepEqual(old.Controller.History(), history) {
		t.Fatal("failed stage changed live state")
	}
}

func TestLiveCompactRatioRejectsInvalidSnapshotAndModel(t *testing.T) {
	old, cfg, root := liveSettingsFixture(t)
	for _, ratio := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		cfg.Agent.CompactRatio = ratio
		next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root, ConfigSnapshot: cfg})
		if err == nil || next != nil || old.Controller.CompactRatio() != .85 {
			t.Fatalf("invalid ratio committed: %v,%v", next, err)
		}
	}
	cfg.Agent.CompactRatio = .8
	next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root, ConfigSnapshot: cfg, Model: "missing-model"})
	if !errors.Is(err, ErrUnknownModel) || next != nil || old.Controller.CompactRatio() != .85 {
		t.Fatalf("invalid model reused runtime: %v,%v", next, err)
	}
}

func TestRuntimeConstructionChangeStillReplacesController(t *testing.T) {
	old, cfg, root := liveSettingsFixture(t)
	cfg.Agent.Temperature += .1
	next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root, ConfigSnapshot: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Controller.Close)
	if next.ReusedController || next.Controller == old.Controller {
		t.Fatal("cached agent options silently reused")
	}
}

func TestSkillOptionsSnapshotLiveCompactRatio(t *testing.T) {
	live := newLiveRuntimeSettings(.85)
	factory := live.skillOptions(func(context.Context, int, *provider.Pricing, int, int) agent.Options {
		return agent.Options{CompactRatio: .85}
	})
	before := factory(t.Context(), 0, nil, 100_000, 1)
	live.apply(.8)
	after := factory(t.Context(), 0, nil, 100_000, 1)
	if before.CompactRatio != .85 || after.CompactRatio != .8 || before.CompactRatioSource != nil || after.CompactRatioSource != nil {
		t.Fatal("skill children did not snapshot threshold at creation")
	}
}

func TestLiveCompactRatioRepeatedUpdatesKeepController(t *testing.T) {
	current, cfg, root := liveSettingsFixture(t)
	original := current.Controller
	for _, ratio := range []float64{.8, .7, .85} {
		cfg.Agent.CompactRatio = ratio
		next, err := RebuildFrom(t.Context(), current, Options{WorkspaceRoot: root, ConfigSnapshot: cfg})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(next.Controller.Close)
		if !next.ReusedController || next.Controller != original || original.CompactRatio() != ratio {
			t.Fatalf("ratio %v did not update in place", ratio)
		}
		current = next
	}
}

func TestRuntimeSelectionSnapshotIsDetached(t *testing.T) {
	effort := "low"
	selection := runtimeSelectionFrom(Options{Model: "fixture/model", EffortOverride: &effort})
	effort = "high"
	if selection == runtimeSelectionFrom(Options{Model: "fixture/model", EffortOverride: &effort}) {
		t.Fatal("caller effort mutation aliased build snapshot")
	}
	if selection == runtimeSelectionFrom(Options{Model: "fixture/other", EffortOverride: &effort}) {
		t.Fatal("caller model change did not invalidate selection")
	}
	if runtimeSelectionFrom(Options{}) == runtimeSelectionFrom(Options{EffortOverride: new(string)}) {
		t.Fatal("explicit empty effort lost its distinction from provider default")
	}
}

func TestLiveCompactRatioExplicitCurrentModelKeepsController(t *testing.T) {
	old, cfg, root := liveSettingsFixture(t)
	cfg.Agent.CompactRatio = .8
	next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root, ConfigSnapshot: cfg, Model: old.Controller.ModelRef()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Controller.Close)
	if !next.ReusedController || next.Controller != old.Controller || next.Controller.CompactRatio() != .8 {
		t.Fatal("making the current model explicit rebuilt the session")
	}
}
