package cli

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

// tuiSessionReclaimedMsg tells the live TUI that the takeover mirror has
// yielded. It is deliberately separate from tuiShutdownMsg: reclaiming one
// session must not terminate the process that may still host other sessions.
type tuiSessionReclaimedMsg struct{}

// reclaimState is the chatTUI substate a remote take-back owns: the three
// flags are set when the mirror yields and cleared together when the TUI
// adopts another session, so they never disagree about who writes.
type reclaimState struct {
	// sessionReclaimed means the remote side owns the previously active
	// session. The TUI stays alive, but input is restricted to leaving it.
	sessionReclaimed bool
	// reclaimedTarget retains the session that was just yielded after its
	// controller binding is released, so the picker can select another row.
	reclaimedTarget cliResumeTarget
	// shutdownAfterReclaim records a signal or watchdog exit request that
	// arrived mid-reclaim; it is honored once the reclaim callback lands
	// instead of racing the handoff.
	shutdownAfterReclaim bool
}

const sessionReclaimedNotice = "this session was taken back by the remote side; /resume switches to another session, /takeover takes it back, /quit exits"

// sessionDetached reports whether the remote side owns the session this TUI
// last wrote: either the reclaim callback has landed or the mirror manager has
// already returned it. A detached TUI must not snapshot or lease-follow the
// session it no longer writes.
func (m *chatTUI) sessionDetached() bool {
	return m.sessionReclaimed || m.takeover != nil && m.takeover.Returned()
}

// completeSessionReclaim applies the manager's yield to the model, then honors
// an exit request that arrived while the handoff was still in flight.
func (m chatTUI) completeSessionReclaim() (tea.Model, tea.Cmd) {
	m.handleSessionReclaimed()
	if m.shutdownAfterReclaim {
		m.shutdownAfterReclaim = false
		return m.shutdownAndQuit(tuiShutdownMsg{})
	}
	return m, nil
}

func (m *chatTUI) handleSessionReclaimed() {
	if m == nil || m.sessionReclaimed {
		return
	}
	m.rememberReclaimedTarget()
	if current, ok := m.ctrl.(*control.Controller); ok {
		m.releaseCanonicalRuntime(current)
	}
	m.sessionReclaimed = true
	// A picker opened before the reclaim listed the yielded session as
	// switchable; its rows and default selection are stale now.
	m.resumePick = nil
	m.resetComposerInput()
	// Keep the conversation rendered and the process alive: the notice explains
	// the read-only state and the input gate accepts only switch/takeover/exit,
	// so the user picks the next step instead of landing in the chooser.
	m.notice(sessionReclaimedNotice)
}

// rememberReclaimedTarget records where "/takeover takes it back" must go.
// The yielded mirror's own key decides: a legacy path lease is re-taken
// through the serve's path handoff even though the exclusive engine imports
// the transcript under an identity (so SessionRef is bound for both kinds), a
// canonical route through the identity handoff. Without a yielded mirror the
// bound identity, then the legacy path, stand in.
func (m *chatTUI) rememberReclaimedTarget() {
	if m == nil || m.ctrl == nil {
		return
	}
	yielded := m.takeover.yieldedBinding()
	if yielded != nil && !yielded.canonical {
		m.reclaimedTarget = cliResumeTarget{path: yielded.path}
		return
	}
	if identity, ok := m.ctrl.(control.IdentityLifecycle); ok {
		if yielded != nil {
			if id, ok := cliCanonicalRouteID(yielded.path); ok {
				if service := identity.SessionService(); service != nil {
					m.reclaimedTarget = cliResumeTarget{ref: session.SessionRef{HostID: service.HostID(), SessionID: id}}
					return
				}
			}
		}
		if ref, bound := identity.SessionRef(); bound {
			m.reclaimedTarget = cliResumeTarget{ref: ref}
			return
		}
	}
	if path := strings.TrimSpace(m.ctrl.SessionPath()); path != "" {
		m.reclaimedTarget = cliResumeTarget{path: path}
	}
}

func (m *chatTUI) releaseCanonicalRuntime(current *control.Controller) {
	if current == nil {
		return
	}
	service, runtime, exclusive := current.SessionBinding()
	if !exclusive || service == nil || runtime == nil {
		return
	}
	ref := runtime.Ref()
	release := func(candidate *control.Controller) {
		if candidate == nil {
			return
		}
		candidateService, candidateRuntime, candidateExclusive := candidate.SessionBinding()
		if candidateExclusive && candidateService == service && candidateRuntime == runtime {
			if err := candidate.ReleaseSessionRuntimeBinding(); err != nil {
				m.notice("session reclaim: release runtime binding: " + err.Error())
			}
		}
	}

	// Rebuilt controllers can retain a client binding until process teardown.
	// Drop every binding for this exact runtime before asking the service to
	// close it, otherwise its writer lock would remain held by an old model.
	release(current)
	for _, old := range m.oldControllers {
		if candidate, ok := old.(*control.Controller); ok {
			release(candidate)
		}
	}
	if err := service.Close(context.Background(), ref); err != nil {
		m.notice("session reclaim: release writer: " + err.Error())
	}
}

func (m *chatTUI) resumeAfterReclaim() {
	if m == nil {
		return
	}
	m.sessionReclaimed = false
	m.reclaimedTarget = cliResumeTarget{}
	if m.takeover != nil {
		m.takeover.ResumeAfterYield()
	}
}

// reclaimBlocksInput reports whether a composer line must be refused because
// the remote side owns this session. A refusal clears the composer and
// explains itself, so the caller only has to finalize the frame.
func (m *chatTUI) reclaimBlocksInput(line string) bool {
	if !m.sessionDetached() || reclaimInputAllowed(line) {
		return false
	}
	m.resetComposerInput()
	m.notice(sessionReclaimedNotice)
	return true
}

// slashInputBlockedNotice returns the refusal for a slash command typed while
// the remote side is taking this session back or already owns it; the empty
// string means the command may run.
func (m *chatTUI) slashInputBlockedNotice(typedCmd string) string {
	switch {
	case m.sessionDetached() && !reclaimInputAllowed(typedCmd):
		return sessionReclaimedNotice
	case m.takeover != nil && m.takeover.Reclaiming() && typedCmd != "/quit" && typedCmd != "/exit":
		return "the remote side is taking this session back; new input is disabled"
	}
	return ""
}

func reclaimInputAllowed(line string) bool {
	command := strings.TrimSpace(strings.SplitN(line, " ", 2)[0])
	switch command {
	case "/resume", "/takeover", "/quit", "/exit":
		return true
	default:
		return false
	}
}
