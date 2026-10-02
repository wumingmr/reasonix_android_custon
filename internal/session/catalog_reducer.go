package session

import "strings"

// Retain only stable IDs and short authored previews. Upserts can erase the
// first preview, and history replacement can reorder it, so keeping only the
// first message would be incorrect. No message/tool/model body survives apply.
type catalogReducer struct {
	state           Projection
	endedTurns      map[string]bool
	resultSequences map[string]uint64
	positions       map[string]bool
}

func (r *catalogReducer) apply(commit Commit) error {
	for _, ev := range commit.Events {
		if ev.Kind == "message/complete" && r.positions[eventMessageID(ev)] {
			r.state.CommittedSequence = ev.Sequence
			continue
		}
		one := commit
		one.Events = []Event{ev}
		// Reuse the canonical payload validators and turn/config semantics.
		if err := applyProjectionCommit(&r.state, one); err != nil {
			return err
		}
		if ev.Kind == "history/replace" || ev.Kind == "legacy/import" {
			r.positions = nil
		}
		if r.positions == nil {
			r.positions = map[string]bool{}
		}
		if ev.Kind == "message/retract" {
			ids, err := retractedMessageIDs(ev, ev.Payload)
			if err != nil {
				return err
			}
			for _, id := range ids {
				delete(r.positions, id)
			}
		}
		for _, message := range r.state.Messages {
			r.positions[strings.Clone(message.ID)] = true
		}
		for _, turn := range r.state.Turns {
			if turn.EndSequence != 0 {
				if r.endedTurns == nil {
					r.endedTurns = map[string]bool{}
				}
				r.endedTurns[turn.TurnID] = true
				if turn.Status.Terminal() && turn.MessageID != "" {
					if r.resultSequences == nil {
						r.resultSequences = map[string]uint64{}
					}
					r.resultSequences[turn.TurnID] = turn.BoundarySequence
				}
			}
		}
		// Keep only state used by subsequent metadata events. Body-heavy state,
		// closed turns and authority maps belong to runtime/history projections.
		s := r.state
		r.state = Projection{CommittedSequence: s.CommittedSequence, TurnID: s.TurnID,
			CurrentTurnStart: s.CurrentTurnStart, CurrentTurnMessageID: s.CurrentTurnMessageID, TurnStatus: s.TurnStatus,
			Title: s.Title, TitleSequence: s.TitleSequence,
			ModelRef: s.ModelRef, ModelIdentity: s.ModelIdentity,
			TranscriptInputs: s.TranscriptInputs, HiddenTurns: s.HiddenTurns, RetractedInputs: s.RetractedInputs}
	}
	return nil
}

func (r *catalogReducer) metadata(manifest Manifest) catalogMetadata {
	m := metadataFromProjection(manifest, r.state.CommittedSequence, r.state)
	for id := range r.endedTurns {
		if !r.state.HiddenTurns[id] {
			m.Turns++
		}
	}
	for id, sequence := range r.resultSequences {
		if !r.state.HiddenTurns[id] {
			m.ResultSequence = max(m.ResultSequence, sequence)
		}
	}
	return m
}
