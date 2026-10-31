package cli

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/i18n"
)

func (m *chatTUI) handleModelSwitch(msg modelSwitchMsg) []tea.Cmd {
	var cmds []tea.Cmd
	if msg.resumeTurn != nil && msg.err == nil && m.ctrl != msg.oldCtrl {
		// The user left the originating session while the replacement built.
		if concrete, ok := msg.ctrl.(*control.Controller); ok {
			concrete.ReleaseResources()
		} else if msg.ctrl != nil {
			msg.ctrl.Close()
		}
		// The deferred carry belonged to the abandoned candidate.
		m.pendingSwitchCarry = nil
		return nil
	}
	m.modelSwitchPending = false
	m.pendingModelSwitch = nil
	// Whatever settles below consumes or discards the deferred carry; clearing it
	// first keeps one switch's conversation from leaking into the next.
	pending := m.pendingSwitchCarry
	m.pendingSwitchCarry = nil
	if msg.err != nil {
		prefix := msg.failurePrefix
		if prefix == "" {
			prefix = "model"
		}
		m.notice(prefix + ": " + msg.err.Error())
		// Build failed — no old controller to retire. The kept controller
		// may still have been retargeted to a recovery branch by the
		// pre-switch snapshot, so the lease must follow it.
		m.followSessionLease()
	} else {
		if err := control.ActivateSessionAPIReplacement(msg.oldCtrl, msg.ctrl); err != nil {
			if concrete, ok := msg.ctrl.(*control.Controller); ok {
				concrete.ReleaseResources()
			} else if msg.ctrl != nil {
				msg.ctrl.Close()
			}
			m.notice("runtime activation: " + err.Error())
			m.followSessionLease()
			return cmds
		}
		// The rebuilt controller owns execution now, so a deferred carried
		// conversation can finally be written. Writing it during the build was
		// silently dropped: a canonical session fences that commit on execution
		// ownership, which only activation confers.
		pending.apply(concreteController(msg.ctrl))
		m.ctrl = activateGoalDriverAfterRebuild(msg.ctrl)
		if m.takeover != nil {
			m.takeover.AttachController(msg.ctrl)
		}
		m.updateWatchdogStatusProvider()
		m.label = msg.label
		m.commands = msg.commands
		m.skills = msg.skills
		m.setHostAndInvalidateSlashCatalog(msg.host)
		m.modelRef = msg.ref
		m.refreshEffortStatus()
		// Defer Close to exit; skip when subgraph rebuild reused the pointer.
		if msg.oldCtrl != nil && msg.oldCtrl != msg.ctrl {
			m.oldControllers = append(m.oldControllers, msg.oldCtrl)
		}
		// Follow the session file, including a recovery branch created by
		// the pre-switch snapshot.
		m.followSessionLease()
		if msg.successNotice != "" {
			m.notice(msg.successNotice)
		} else {
			m.notice(fmt.Sprintf(i18n.M.ModelSwitchedFmt, m.label))
		}
		cmds = append(cmds, fetchBalance(m.ctrl))
		if msg.resumeTurn != nil {
			cmds = append(cmds, m.resumeControllerTurn(*msg.resumeTurn, false))
		}
		if c := m.runStatusline(); c != nil {
			cmds = append(cmds, c)
		}
		// Keep the existing waitForAgentEvent reader: a second consumer
		// would race it and deliver streamed events out of order.
	}

	// A /reload queued behind this switch runs now that it settled. On a
	// failed switch the old controller still serves, so the reload simply
	// retries against it.
	if c := m.drainQueuedRuntimeReload(); c != nil {
		cmds = append(cmds, c)
	}
	return cmds
}

// concreteController narrows a SessionAPI to the concrete controller a deferred
// history write needs, or nil when the implementation owns no session store.
func concreteController(api control.SessionAPI) *control.Controller {
	c, _ := api.(*control.Controller)
	return c
}
