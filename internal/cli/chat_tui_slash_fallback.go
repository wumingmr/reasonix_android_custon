package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/i18n"
	"reasonix/internal/skill"
)

// runUnrecognizedSlash resolves slash input that is not a built-in command, in
// the order a name must win: built-in docs pages, then a custom command, then
// a skill, then an extension action, and finally prose sent as a plain message.
// cmd is the canonicalized spelling used in the unknown-command notice.
func (m *chatTUI) runUnrecognizedSlash(input, typedCmd, cmd string) tea.Cmd {
	if control.IsBuiltinDocsSlash(typedCmd, m.commands, m.skills) {
		query := strings.TrimSpace(strings.TrimPrefix(input, typedCmd))
		if query != "" {
			return m.startControllerTurn(input, input, func(ctrl control.SessionAPI) { ctrl.SubmitDisplay(input, input) })
		}
		m.echoLocalCommand(input)
		text, err := control.DocsCommandOverviewFor(typedCmd)
		if err != nil {
			m.notice("docs: " + err.Error())
		} else {
			m.commitLine(text)
		}
		return nil
	}
	// A custom command wins over a skill of the same name; both resolve to a turn.
	if sent, ok := m.ctrl.CustomCommand(input); ok {
		return m.startTurn(sent, input, input)
	}
	if _, ok := m.ctrl.RunSkill(input); ok {
		fields := strings.Fields(input)
		name := strings.TrimPrefix(fields[0], "/")
		for _, sk := range m.ctrl.Skills() {
			if sk.Name == name && sk.RunAs == skill.RunSubagent && len(fields) == 1 {
				m.echoLocalCommand(input)
				m.notice("usage: /" + name + " <task>")
				return nil
			}
		}
		return m.startControllerTurn(input, input, func(ctrl control.SessionAPI) { ctrl.SubmitDisplay(input, input) })
	}
	// An extension action (/<plugin>:<action>) resolves last, before the
	// unknown-command fallback; the invocation is a sidecar round-trip, so it
	// runs off the event loop and its result lands as a notice.
	if action, ok := matchExtensionAction(m.ctrl, typedCmd); ok {
		m.echoLocalCommand(input)
		return m.runExtensionAction(action.Slash, parseExtensionActionArgs(strings.Fields(input)[1:]))
	}
	// Unknown slash input is prose more often than a typo — send it as a
	// regular message (matching the controller's behavior for the other
	// surfaces), with a notice so real typos stay visible (#5756).
	m.notice(fmt.Sprintf("%s: %s — %s", i18n.M.SlashUnknown, cmd, i18n.M.SlashUnknownSentAsMessage))
	return m.startTurn(input, input, input)
}
