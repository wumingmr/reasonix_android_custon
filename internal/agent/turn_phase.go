package agent

import (
	"time"

	"reasonix/internal/event"
)

type phaseClock struct {
	last event.TurnPhaseName
	at   time.Time
}

// emitTurnPhase publishes a content-free host phase for the active turn.
func (a *Agent) emitTurnPhase(phase event.TurnPhaseName) {
	if a == nil || a.svc.sink == nil || phase == "" {
		return
	}
	now := time.Now()
	a.recordOpenPhase(now)
	a.turn.phase = phaseClock{last: phase, at: now}
	a.svc.sink.Emit(event.Event{Kind: event.TurnPhase, PhaseName: phase, Text: string(phase)})
}

// recordOpenPhase bills the phase that is ending. A span is accounted for only
// when something closes it, so a phase nobody follows records nothing.
func (a *Agent) recordOpenPhase(now time.Time) {
	if a.turn.phase.at.IsZero() || a.capabilityAudit == nil {
		return
	}
	a.capabilityAudit.RecordPhaseMs(phaseAuditName(a.turn.phase.last), now.Sub(a.turn.phase.at).Milliseconds())
}

// closeTurnPhase bills the open phase without opening another one, for the end
// of a turn: beginRunTurn resets a.turn, so an unclosed tail span is lost.
func (a *Agent) closeTurnPhase() {
	if a == nil {
		return
	}
	a.recordOpenPhase(time.Now())
	a.turn.phase = phaseClock{}
}

func phaseAuditName(phase event.TurnPhaseName) string {
	switch phase {
	case event.TurnPhaseWorking:
		return "provider"
	case event.TurnPhaseChecking, event.TurnPhaseVerifying:
		return "tool"
	case event.TurnPhaseReviewing:
		return "review"
	default:
		return ""
	}
}
