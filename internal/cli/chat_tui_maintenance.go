package cli

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"strconv"
	"strings"
)

func (m chatTUI) maintenanceCancellable() bool {
	if m.maintenance == nil || m.ctrl == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(m.maintenance.Activity)) {
	case "", "starting", "running":
		return true
	default:
		return false
	}
}

func (m chatTUI) runningWorkingLine(cancelRequested, styled bool) string {
	if m.state != tuiRunning {
		if m.maintenance == nil {
			return ""
		}
		var label string
		switch strings.ToLower(strings.TrimSpace(m.maintenance.Activity)) {
		case "cancelling":
			label = i18n.M.CompactionStopping
		case "finalizing":
			label = i18n.M.CompactionSaving
		case "recovery_required":
			label = i18n.M.CompactionRecoveryRequired
		default:
			label = i18n.M.CompactionWorking
		}
		return fmt.Sprintf("  %s %s", m.spinner.View(), label)
	}
	if m.retryAttempt > 0 && !cancelRequested {
		if line, ok := m.waitingRecoveryLine(); ok {
			return line
		}

		return fmt.Sprintf("  "+i18n.M.ChatStatusRetryingFmt, m.spinner.View(), m.retryAttempt, m.retryMax)
	}

	var working string
	if cancelRequested {
		working = fmt.Sprintf("  "+i18n.M.ChatStatusCancellingFmt, m.spinner.View(), m.elapsed)
	} else {
		phaseLabel := m.readStatusLabel
		if phaseLabel == "" {
			phaseLabel = turnPhaseStatusLabel(m.turnPhase)
		}
		if phaseLabel != "" {
			working = fmt.Sprintf("  %s %s · %s", m.spinner.View(), phaseLabel, m.elapsedUnitLabel())
		} else {
			working = fmt.Sprintf("  "+i18n.M.ChatStatusThinkingFmt, m.spinner.View(), m.elapsed)
		}
	}
	if m.turnTokens > 0 {
		working += " · ↓" + shortTokens(m.turnTokens)
	}
	if n := m.inboxQueuedCount(); n > 0 {
		var queued string
		if n == 1 {
			queued = " · ✎ 1 in inbox"
		} else {
			queued = fmt.Sprintf(" · ✎ %d in inbox", n)
		}
		if m.inboxSnap().Paused {
			queued += " (paused)"
		}
		if styled {
			working += dim(queued)
		} else {
			working += queued
		}
	}
	return working
}

func (m *chatTUI) runCompactCommand(input, typedCmd string) tea.Cmd {
	m.echoLocalCommand(input)
	// Register maintenance before returning so Esc and new input see its owner.
	// The worker runs in the background; trailing text remains summary guidance.
	if submitter, ok := m.ctrl.(interface {
		SubmitDisplayWithResult(display, input string) control.SubmitResult
	}); ok {
		result := submitter.SubmitDisplayWithResult(input, input)
		if result.OperationID != "" {
			op := &event.SessionOperationInfo{
				OperationID: result.OperationID,
				Kind:        "compact",
				Activity:    "running",
				Status:      "running",
			}
			m.maintenance = op
			m.renderSessionOperation(op)
		}
		return nil
	}

	// Compatibility path for older SessionAPI implementations. Keep an
	// explicit maintenance placeholder so Esc preserves the draft while the
	// blocking Compact call runs as a Bubble Tea command.
	focus := strings.TrimSpace(strings.TrimPrefix(input, typedCmd))
	m.maintenance = &event.SessionOperationInfo{Kind: "compact", Activity: "starting", Status: "running"}
	m.compactCompatibilityPending = true
	m.compactLifecycleObserved = false
	return func() tea.Msg { return compactDoneMsg{err: m.ctrl.Compact(context.Background(), focus)} }
}

func (m *chatTUI) stopMaintenance() {
	m.ctrl.Cancel()
	updated := *m.maintenance
	updated.Activity = "cancelling"
	updated.Status = "cancelling"
	m.maintenance = &updated
	m.renderSessionOperation(&updated)
}

// elapsedUnitLabel renders the turn timer with its unit. With a frozen spinner
// (ui.spinner = "static") the phase line's seconds are the only thing still
// changing once a second, and on Termux every change costs a redraw that
// fights the reader scrolling back through the transcript. Reporting minutes
// keeps the line still for most of a turn; other terminals keep seconds, where
// a 1 Hz update hides behind the animation.
//
// m.elapsed itself stays in seconds — only the rendered unit changes.
func (m *chatTUI) elapsedUnitLabel() string {
	if m.coarseElapsedActive() {
		return strconv.Itoa(m.elapsed/60) + "m"
	}
	return strconv.Itoa(m.elapsed) + "s"
}
