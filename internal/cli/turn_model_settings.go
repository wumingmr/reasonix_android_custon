package cli

import (
	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
)

// An intent has not created a turn or run any submission hook. Its callback
// receives the activated controller, never the controller captured before I/O.
type controllerTurnIntent struct {
	displayed, restore, queued string
	start                      func(control.SessionAPI)
}

type turnModelSettingsMsg struct {
	ctrl    control.SessionAPI
	intent  *controllerTurnIntent
	changed bool
	err     error
}

func (m *chatTUI) checkTurnModelSettings(intent controllerTurnIntent) (tea.Cmd, bool) {
	if m.modelSwitchPending || m.turnSettingsIntent != nil {
		m.restoreTurnDraft(intent)
		m.notice("model settings are being applied; your draft has been preserved")
		return nil, true
	}
	if owner, ok := m.ctrl.(interface{ TracksModelSettings() bool }); ok && !owner.TracksModelSettings() {
		return nil, false
	}
	reader, ok := m.ctrl.(interface {
		ModelSettingsState() (string, string, error)
	})
	if !ok {
		return nil, false
	}
	ctrl := m.ctrl
	m.turnSettingsIntent = &intent
	m.restoreTurnDraft(intent)
	return func() tea.Msg {
		applied, desired, err := reader.ModelSettingsState()
		return turnModelSettingsMsg{ctrl: ctrl, intent: &intent, changed: applied != desired, err: err}
	}, true
}

func (m *chatTUI) restoreTurnDraft(intent controllerTurnIntent) {
	if m.input.Value() == "" {
		m.input.SetValue(intent.restore)
		m.growInputToFit()
	}
}

func (m *chatTUI) handleTurnModelSettings(msg turnModelSettingsMsg) tea.Cmd {
	if m.turnSettingsIntent != msg.intent {
		return nil
	}
	m.turnSettingsIntent = nil
	if m.ctrl != msg.ctrl || m.modelSwitchPending {
		return nil
	}
	if msg.err != nil {
		m.notice("model settings: " + msg.err.Error())
		return nil
	}
	if msg.changed {
		if m.modelReplacementBusy() {
			m.notice("saved model settings are waiting for runtime-dependent work; your draft has been preserved")
			return nil
		}
		cmd := m.scheduleCurrentControllerRebuild("model settings", "saved model settings applied")
		if cmd == nil {
			return nil
		}
		return func() tea.Msg {
			result := cmd().(modelSwitchMsg)
			result.resumeTurn = msg.intent
			return result
		}
	}
	return m.resumeControllerTurn(*msg.intent, true)
}

func (m *chatTUI) resumeControllerTurn(intent controllerTurnIntent, checked bool) tea.Cmd {
	if m.input.Value() == intent.restore {
		m.input.SetValue("")
	}
	return m.prepareControllerTurn(intent, checked)
}
