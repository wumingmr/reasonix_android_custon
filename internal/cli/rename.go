package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/i18n"
	"reasonix/internal/session"
)

// sessionTitleWriter is the controller's canonical title owner for the
// session it is bound to.
type sessionTitleWriter interface {
	SetSessionTitle(context.Context, string) error
}

// runRenameCommand handles "/rename": with no argument it shows usage;
// "/rename <new title>" renames the current session;
// "/rename <n> <new title>" renames session #n from the /resume list.
func (m *chatTUI) runRenameCommand(input string) {
	args := tokenizeArgs(input) // args[0] == "/rename"

	if len(args) < 2 {
		m.notice(i18n.M.RenameUsage)
		return
	}

	var target cliResumeTarget
	title := ""

	// Check if the first arg after /rename is a session index (a number).
	idx, err := strconv.Atoi(args[1])
	if err == nil && len(args) >= 3 {
		// "/rename <n> <new title>"
		sessions := mergedResumeEntries(m.ctrl.SessionDir(), resumeListCap)
		if idx < 1 || idx > len(sessions) {
			m.notice(fmt.Sprintf(i18n.M.ResumeBadIndexFmt, len(sessions)))
			return
		}
		picked := sessions[idx-1]
		target = picked.target
		if !target.canonical() {
			target.path = picked.session.Path
		}
		title = strings.TrimSpace(strings.TrimPrefix(input, args[0]+" "+args[1]))
	} else {
		// "/rename <new title>" -- rename the current session.
		target = m.currentRenameTarget()
		if target.empty() {
			m.notice(i18n.M.RenameNoSession)
			return
		}
		title = strings.TrimSpace(strings.TrimPrefix(input, args[0]))
	}

	if title == "" {
		m.notice(i18n.M.RenameUsage)
		return
	}

	if err := m.renameSession(target, title); err != nil {
		m.notice("rename: " + err.Error())
		return
	}

	m.notice(fmt.Sprintf(i18n.M.RenameDoneFmt, title))
}

// currentRenameTarget names the session the controller writes: its canonical
// identity when bound to one, otherwise its legacy transcript path.
func (m *chatTUI) currentRenameTarget() cliResumeTarget {
	if identity, ok := m.ctrl.(control.IdentityLifecycle); ok {
		if ref, bound := identity.SessionRef(); bound {
			return cliResumeTarget{ref: ref}
		}
	}
	return cliResumeTarget{path: m.ctrl.SessionPath()}
}

// renameSession writes the title to whoever owns it: the bound controller for
// its own session, the session service for any other canonical session, and
// the legacy sidecar for a transcript path.
func (m *chatTUI) renameSession(target cliResumeTarget, title string) error {
	if !target.canonical() {
		return agent.RenameSession(target.path, title)
	}
	ctx := context.Background()
	if identity, ok := m.ctrl.(control.IdentityLifecycle); ok {
		if ref, bound := identity.SessionRef(); bound && ref == target.ref && identity.UsesExclusiveSession() {
			if writer, ok := m.ctrl.(sessionTitleWriter); ok {
				return writer.SetSessionTitle(ctx, title)
			}
		}
		if service := identity.SessionService(); service != nil {
			return service.SetTitle(ctx, target.ref, title)
		}
	}
	if service := cliSessionService(m.ctrl.SessionDir()); service != nil {
		return service.SetTitle(ctx, target.ref, title)
	}
	return session.ErrSessionNotRunning
}
