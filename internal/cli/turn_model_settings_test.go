package cli

import (
	"errors"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/provider"
)

func TestTurnAppliesExternalCredentialSaveBeforeAuthentication(t *testing.T) {
	for _, failBuild := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failBuild], func(t *testing.T) {
			reads := 0
			old := control.New(control.Options{ModelSettingsRevision: "old", ModelSettingsCurrent: func() (string, error) { reads++; return "new", nil }, Authentication: control.AuthenticationState{Status: control.AuthenticationMissingCredential}})
			defer old.Close()
			fresh := control.New(control.Options{ModelSettingsRevision: "new", ModelSettingsCurrent: func() (string, error) { return "new", nil }})
			defer fresh.Close()
			m := newTestChatTUI()
			m.ctrl = old
			m.buildController = func(controllerBuildSpec, []provider.Message, string, control.SessionAPI) (*control.Controller, error) {
				if failBuild {
					return nil, errors.New("fixture build failure")
				}
				return fresh, nil
			}
			var started control.SessionAPI
			cmd := m.startControllerTurn("draft", "draft", func(c control.SessionAPI) { started = c })
			if cmd == nil || reads != 0 {
				t.Fatal("settings must be checked asynchronously")
			}
			cmd = m.handleTurnModelSettings(cmd().(turnModelSettingsMsg))
			if reads != 1 || cmd == nil || started != nil {
				t.Fatal("old missing-key controller prevented rebuild")
			}
			switchMsg := cmd().(modelSwitchMsg)
			if failBuild {
				if switchMsg.err == nil || m.input.Value() != "draft" || m.ctrl != old {
					t.Fatal("failed rebuild lost original work")
				}
				return
			}
			// Exercise the same activation and next-admission sequence without
			// unrelated terminal, wallet, or filesystem commands from Update.
			m.ctrl = switchMsg.ctrl
			m.modelSwitchPending = false
			cmd = m.resumeControllerTurn(*switchMsg.resumeTurn, false)
			m.handleTurnModelSettings(cmd().(turnModelSettingsMsg))
			if started != fresh || m.input.Value() != "" {
				t.Fatal("submission did not use replacement controller")
			}
		})
	}
}

func TestTurnSettingsCompletionCannotStartAnotherSession(t *testing.T) {
	old := control.New(control.Options{ModelSettingsRevision: "one", ModelSettingsCurrent: func() (string, error) { return "one", nil }})
	defer old.Close()
	fresh := control.New(control.Options{})
	defer fresh.Close()
	m := newTestChatTUI()
	m.ctrl = old
	cmd := m.startControllerTurn("draft", "draft", func(control.SessionAPI) { t.Fatal("stale submission started") })
	m.ctrl = fresh
	if got := m.handleTurnModelSettings(cmd().(turnModelSettingsMsg)); got != nil {
		t.Fatal("stale completion scheduled work")
	}
}

func TestTurnSettingsCompletionCannotRaceAnotherRuntimeSwitch(t *testing.T) {
	ctrl := control.New(control.Options{ModelSettingsRevision: "one", ModelSettingsCurrent: func() (string, error) { return "one", nil }})
	defer ctrl.Close()
	m := newTestChatTUI()
	m.ctrl = ctrl
	cmd := m.startControllerTurn("draft", "draft", func(control.SessionAPI) { t.Fatal("submission raced runtime switch") })
	m.modelSwitchPending = true
	if got := m.handleTurnModelSettings(cmd().(turnModelSettingsMsg)); got != nil || m.input.Value() != "draft" {
		t.Fatal("switch discarded draft or admitted stale turn")
	}
}
