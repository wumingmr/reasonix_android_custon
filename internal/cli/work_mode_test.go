package cli

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

func resetPresetDeprecationForTest() {
	presetDeprecationOnce = sync.Once{}
}

func committedNotices(m chatTUI) string {
	if m.pendingCommit == nil {
		return ""
	}
	return ansi.Strip(strings.Join(*m.pendingCommit, "\n"))
}

func TestParseAgentPresetAcceptsLegacyLabels(t *testing.T) {
	for input, want := range map[string]string{
		"economy":  "standard",
		"light":    "standard",
		"eco":      "standard",
		"lite":     "standard",
		"balanced": "standard",
		"full":     "standard",
		"standard": "standard",
		"delivery": "standard",
		"deliver":  "standard",
	} {
		got, ok := parseAgentPreset(input)
		if !ok || got != want {
			t.Errorf("parseAgentPreset(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	if _, ok := parseAgentPreset("unknown"); ok {
		t.Fatal("unknown preset should be rejected")
	}
}

func TestRetiredPresetCommandsAreHiddenFromCompletion(t *testing.T) {
	m := newTestChatTUI()
	for _, command := range []string{"/preset", "/profile", "/work-mode"} {
		if hasLabel(m.slashItems(), command) {
			t.Fatalf("retired compatibility command %q appeared in slash completion", command)
		}
	}
	for _, input := range []string{"/preset ", "/work-mode ", "/profile "} {
		if _, _, ok := m.slashArgItems(input); ok {
			t.Fatalf("%q offered retired execution-mode argument completion", input)
		}
	}
}

func TestConsumeDeprecatedModeFlagsKeepsCompatibilityOutOfPublicFlagSets(t *testing.T) {
	clean, mode, err := consumeDeprecatedModeFlags([]string{
		"--model", "deepseek", "--profile=delivery", "fix it", "--preset", "light",
	}, "profile", "preset")
	if err != nil {
		t.Fatal(err)
	}
	if mode != "delivery" {
		t.Fatalf("mode = %q, want profile precedence delivery", mode)
	}
	if got := strings.Join(clean, " "); got != "--model deepseek fix it" {
		t.Fatalf("clean args = %q", got)
	}

	clean, mode, err = consumeDeprecatedModeFlags([]string{"run", "--", "--profile", "delivery"}, "profile")
	if err != nil || mode != "" || strings.Join(clean, " ") != "run -- --profile delivery" {
		t.Fatalf("post-boundary args: clean=%v mode=%q err=%v", clean, mode, err)
	}
	if _, _, err := consumeDeprecatedModeFlags([]string{"--profile"}, "profile"); err == nil {
		t.Fatal("missing compatibility flag value must fail")
	}
}

func TestLegacyModeFlagsAreHiddenFromCommandHelp(t *testing.T) {
	tests := []struct {
		name string
		run  func() int
	}{
		{name: "run", run: func() int { return runAgent([]string{"--help"}, "dev") }},
		{name: "chat", run: func() int { return chatREPL([]string{"--help"}, "dev") }},
		{name: "web", run: func() int { return runWeb([]string{"--help"}) }},
		{name: "acp", run: func() int { return acpCommand([]string{"--help"}, "dev") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				if code := tt.run(); code != 0 {
					t.Fatalf("help exit code = %d, want 0", code)
				}
			})
			if strings.Contains(out, "--profile") || strings.Contains(out, "--preset") {
				t.Fatalf("legacy mode flag leaked into help:\n%s", out)
			}
		})
	}
}

func TestRetiredPresetCommandsAreHiddenFromHelp(t *testing.T) {
	for _, command := range []string{"/preset", "/profile", "/work-mode"} {
		if hasLabel(builtinHelpItems(), command) {
			t.Fatalf("built-in help listed retired command %q", command)
		}
	}
}

func TestPresetTagStaysHiddenForLegacyInputs(t *testing.T) {
	ctrl := newOwnedTestController(t, control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	if tag := m.presetTag(); tag != "" {
		t.Fatalf("standard floor should stay quiet, got %q", tag)
	}
	if err := ctrl.SetQualityFloor(control.QualityFloorDelivery); err != nil {
		t.Fatal(err)
	}
	if tag := m.presetTag(); tag != "" {
		t.Fatalf("retired delivery setting surfaced a footer tag: %q", tag)
	}
}

func TestPresetCommandIsInPlaceCompatibilityNoOp(t *testing.T) {
	resetPresetDeprecationForTest()
	oldCtrl := newOwnedTestController(t, control.Options{Label: "old"})
	oldCtrl.SetToolApprovalMode(control.ToolApprovalAuto)
	oldCtrl.SetPlanMode(true)
	m := newChatTUI(oldCtrl, "", make(chan event.Event, 1), 100)
	m.modelRef = "provider/model"
	builds := 0
	m.buildController = func(controllerBuildSpec, []provider.Message, string, control.SessionAPI) (*control.Controller, error) {
		builds++
		return newOwnedTestController(t, control.Options{Label: "new"}), nil
	}

	cmd := m.runWorkModeCommand("/preset delivery")
	if cmd != nil {
		t.Fatal("/preset must not schedule a controller rebuild")
	}
	if m.ctrl != oldCtrl {
		t.Fatal("controller must stay the same instance")
	}
	if m.ctrl.AgentPreset() != boot.AgentPresetStandard {
		t.Fatalf("controller preset = %q, want standard", m.ctrl.AgentPreset())
	}
	if builds != 0 {
		t.Fatalf("unexpected rebuilds: %d", builds)
	}
	if out := committedNotices(m); !strings.Contains(out, i18n.M.WorkModeDeprecatedNotice) {
		t.Fatalf("legacy /preset missing retirement notice:\n%s", out)
	}

	if cmd := m.runWorkModeCommand("/preset light"); cmd != nil {
		t.Fatal("second /preset must not rebuild")
	}
	if m.ctrl.AgentPreset() != boot.AgentPresetStandard {
		t.Fatalf("light must fold to standard, got %q", m.ctrl.AgentPreset())
	}
}

func TestPresetCommandRejectsInvalidValue(t *testing.T) {
	resetPresetDeprecationForTest()
	m := newTestChatTUI()
	m.ctrl = newOwnedTestController(t, control.Options{Label: "model"})
	m.modelRef = "provider/model"
	builds := 0
	m.buildController = func(controllerBuildSpec, []provider.Message, string, control.SessionAPI) (*control.Controller, error) {
		builds++
		return newOwnedTestController(t, control.Options{Label: "new"}), nil
	}

	if cmd := m.runWorkModeCommand("/preset unknown"); cmd != nil {
		t.Fatal("invalid /preset unexpectedly scheduled a rebuild")
	}
	out := committedNotices(m)
	if !strings.Contains(out, i18n.M.WorkModeUsage) {
		t.Fatalf("invalid /preset missing usage:\n%s", out)
	}
	if m.ctrl.AgentPreset() != boot.AgentPresetStandard {
		t.Fatalf("controller preset = %q, want standard", m.ctrl.AgentPreset())
	}
	if builds != 0 {
		t.Fatalf("rejected preset request triggered %d builds", builds)
	}
}

func TestPresetCommandSwitchesWhenBusy(t *testing.T) {
	resetPresetDeprecationForTest()
	m := newTestChatTUI()
	m.ctrl = newOwnedTestController(t, control.Options{Label: "model"})
	m.modelRef = "provider/model"
	m.pendingApproval = &event.Approval{ID: "approval", Tool: "bash"}
	builds := 0
	m.buildController = func(controllerBuildSpec, []provider.Message, string, control.SessionAPI) (*control.Controller, error) {
		builds++
		return newOwnedTestController(t, control.Options{Label: "new"}), nil
	}
	if cmd := m.runWorkModeCommand("/preset light"); cmd != nil {
		t.Fatal("busy /preset must not rebuild")
	}
	if m.ctrl.AgentPreset() != boot.AgentPresetStandard {
		t.Fatalf("busy /preset AgentPreset = %q, want standard (light folds)", m.ctrl.AgentPreset())
	}
	if builds != 0 {
		t.Fatalf("busy /preset triggered %d builds", builds)
	}
}

func TestPresetCommandSwitchesDuringRunningTurn(t *testing.T) {
	resetPresetDeprecationForTest()
	runner := &blockingTurnRunner{started: make(chan struct{})}
	ctrl := newOwnedTestController(t, control.Options{Runner: runner, Sink: event.Discard, SessionDir: t.TempDir(), Label: "model"})
	ctrl.Send("keep running")
	<-runner.started
	t.Cleanup(func() {
		ctrl.Cancel()
		deadline := time.Now().Add(2 * time.Second)
		for ctrl.Running() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	})

	m := newTestChatTUI()
	m.ctrl = ctrl
	m.modelRef = "provider/model"
	builds := 0
	m.buildController = func(controllerBuildSpec, []provider.Message, string, control.SessionAPI) (*control.Controller, error) {
		builds++
		return newOwnedTestController(t, control.Options{}), nil
	}
	if cmd := m.runWorkModeCommand("/preset delivery"); cmd != nil {
		t.Fatal("running-turn /preset must not rebuild")
	}
	if m.ctrl.AgentPreset() != boot.AgentPresetStandard {
		t.Fatalf("running-turn /preset AgentPreset = %q, want standard", m.ctrl.AgentPreset())
	}
	if builds != 0 {
		t.Fatalf("running-turn /preset triggered %d builds", builds)
	}
	if out := committedNotices(m); !strings.Contains(out, i18n.M.WorkModeDeprecatedNotice) {
		t.Fatalf("running-turn /preset missing retirement notice:\n%s", out)
	}
}
