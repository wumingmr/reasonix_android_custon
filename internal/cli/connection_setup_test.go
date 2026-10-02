package cli

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
)

type connectionSetupTestRunner struct{ calls int }

func (r *connectionSetupTestRunner) Run(context.Context, string) error {
	r.calls++
	return nil
}

func TestConnectionSetupMasksCredential(t *testing.T) {
	m := newTestChatTUI()
	m.width = 80
	m.setup = &connectionSetup{providerName: "deepseek", key: "never-render-this-key"}
	view := m.renderConnectionSetup()
	if strings.Contains(view, m.setup.key) {
		t.Fatal("connection setup rendered the credential")
	}
	if !strings.Contains(view, "Ctrl+T test") {
		t.Fatalf("connection test action missing:\n%s", view)
	}
}

func TestAuthenticationSlashAliases(t *testing.T) {
	if got := canonicalBuiltinSlashCommand("/?"); got != "/help" {
		t.Fatalf("/? = %q, want /help", got)
	}
	if got := canonicalBuiltinSlashCommand("/auth"); got != "/setup" {
		t.Fatalf("/auth = %q, want /setup", got)
	}
}

func TestConnectionSetupInvalidatesEditedProbe(t *testing.T) {
	m := newTestChatTUI()
	cancelled := false
	setup := &connectionSetup{providerName: "relay", key: "old", testing: true, testVersion: 3, testCancel: func() { cancelled = true }}
	m.setup = setup
	updated, _ := m.handleConnectionSetupKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = updated.(chatTUI)
	if !cancelled || m.setup.testing || m.setup.key != "oldx" {
		t.Fatalf("edited setup = %+v, cancelled=%v", m.setup, cancelled)
	}
	before := len(m.transcript)
	m.handleConnectionCredentialTested(connectionCredentialTestedMsg{providerName: "relay", setup: setup, version: 3})
	if len(m.transcript) != before {
		t.Fatal("stale connection test updated the current UI")
	}
}

func TestConnectionSetupConflictKeepsEditorOpen(t *testing.T) {
	m := newTestChatTUI()
	m.setup = &connectionSetup{providerName: "relay", key: "draft", saving: true}
	cmd := m.handleConnectionCredentialSaved(connectionCredentialSavedMsg{providerName: "relay", err: context.Canceled})
	if cmd != nil || m.setup == nil || m.setup.saving || m.setup.key != "draft" {
		t.Fatalf("failed save lost editor state: setup=%+v cmd=%v", m.setup, cmd)
	}
}

func TestConnectionSetupSaveSchedulesRuntimeRebuild(t *testing.T) {
	runner := &connectionSetupTestRunner{}
	ctrl := control.New(control.Options{Runner: runner})
	defer ctrl.Close()
	m := newTestChatTUI()
	m.ctrl = ctrl
	m.modelRef = "relay/chat"
	m.setup = &connectionSetup{providerName: "relay", key: "saved", saving: true}
	m.buildController = func(controllerBuildSpec, []provider.Message, string, control.SessionAPI) (*control.Controller, error) {
		return nil, nil
	}
	cmd := m.handleConnectionCredentialSaved(connectionCredentialSavedMsg{providerName: "relay", result: config.ConnectionCredentialResult{Persisted: true}})
	if cmd == nil || !m.modelSwitchPending || m.setup != nil {
		t.Fatalf("saved credential did not schedule rebuild: pending=%v setup=%+v", m.modelSwitchPending, m.setup)
	}
}

func TestMissingAuthenticationPreservesCLIInputWithoutProviderCall(t *testing.T) {
	runner := &connectionSetupTestRunner{}
	ctrl := control.New(control.Options{Runner: runner, Authentication: control.AuthenticationState{Status: control.AuthenticationMissingCredential, ProviderName: "relay"}})
	defer ctrl.Close()
	m := newTestChatTUI()
	m.ctrl = ctrl
	m.input.SetValue("keep this draft")
	before := len(m.transcript)
	cmd := m.startControllerTurnWithQueue("keep this draft", "keep this draft", "", func(control.SessionAPI) { ctrl.Submit("must not run") })
	if cmd != nil {
		t.Fatal("missing authentication scheduled a model turn")
	}
	if runner.calls != 0 || m.input.Value() != "keep this draft" {
		t.Fatalf("blocked input changed state: calls=%d input=%q", runner.calls, m.input.Value())
	}
	if len(m.transcript) != before+1 {
		t.Fatalf("blocked input should add only a local notice: before=%d after=%d", before, len(m.transcript))
	}
}
