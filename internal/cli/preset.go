package cli

import (
	"sync"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/agentpreset"
	"reasonix/internal/i18n"
)

// /preset, /work-mode, and /profile remain as hidden compatibility commands.
// Recognized legacy values no longer alter the session runtime.

var presetDeprecationOnce sync.Once

// noticePresetFolded prints the one-time fold notice per process.
func (m *chatTUI) noticePresetFolded() {
	presetDeprecationOnce.Do(func() {
		m.notice(i18n.M.WorkModeDeprecatedNotice)
	})
}

// parseAgentPreset validates a retired role argument and returns the standard
// compatibility value for every recognized legacy alias.
func parseAgentPreset(value string) (string, bool) {
	if p, err := agentpreset.Normalize(value); err == nil {
		return string(p), true
	}
	return "", false
}

// runPresetCommand accepts legacy input without changing the controller or
// scheduling a turn.
func (m *chatTUI) runPresetCommand(input string) tea.Cmd {
	args := tokenizeArgs(input)
	if len(args) == 2 {
		floor, ok := parseAgentPreset(args[1])
		if !ok {
			m.notice(i18n.M.WorkModeUsage)
			return nil
		}
		if err := m.ctrl.SetQualityFloor(floor); err != nil {
			m.notice(err.Error())
			return nil
		}
		m.noticePresetFolded()
		return nil
	}
	if len(args) == 1 {
		m.notice(i18n.M.WorkModeUsage)
		return nil
	}
	m.noticePresetFolded()
	return nil
}

// runWorkModeCommand keeps the legacy dispatch name compiling; /work-mode and
// /profile delegate to the same handler.
func (m *chatTUI) runWorkModeCommand(input string) tea.Cmd {
	if len(tokenizeArgs(input)) > 2 {
		m.noticePresetFolded()
		return nil
	}
	return m.runPresetCommand(input)
}
