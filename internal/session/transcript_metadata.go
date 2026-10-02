package session

import (
	"encoding/json"
	"reasonix/internal/provider"
)

// Transcript metadata retains only input identities and bounded previews, never
// assistant/tool bodies. It remains available when display history is external.
type transcriptInput struct {
	ID      string
	TurnID  string
	Preview string
}

func applyTranscriptMetadata(p *Projection, commit Commit, ev Event) {
	switch ev.Kind {
	case "history/replace", "legacy/import":
		p.TranscriptInputs = nil
		p.HiddenTurns = nil
		p.RetractedInputs = nil
		p.RejectedToolResults = nil
		messages, err := replacementEventMessages(ev, ev.Payload)
		if err != nil {
			return
		}
		for _, m := range messages {
			noteTranscriptInput(p, "", m)
			noteRejectedToolResult(p, m)
		}
	case "message/complete", "message/upsert":
		var body struct {
			Message provider.Message `json:"message"`
		}
		if json.Unmarshal(ev.Payload, &body) != nil {
			return
		}
		if body.Message.LocalOnly || body.Message.Role != provider.RoleAssistant {
			for i := range p.Turns {
				if p.Turns[i].MessageID == body.Message.ID {
					p.Turns[i].MessageID = ""
				}
			}
		}
		noteTranscriptInput(p, commit.TurnID, body.Message)
		noteRejectedToolResult(p, body.Message)
	case "message/retract":
		ids, err := retractedMessageIDs(ev, ev.Payload)
		if err != nil {
			return
		}
		for _, id := range ids {
			delete(p.RejectedToolResults, id)
			for i := 0; i < len(p.TranscriptInputs); i++ {
				input := p.TranscriptInputs[i]
				if input.ID != id {
					continue
				}
				if input.TurnID != "" {
					if p.RetractedInputs == nil {
						p.RetractedInputs = map[string]string{}
					}
					p.RetractedInputs[id] = input.TurnID
					if p.HiddenTurns == nil {
						p.HiddenTurns = map[string]bool{}
					}
					p.HiddenTurns[input.TurnID] = true
				}
				p.TranscriptInputs = append(p.TranscriptInputs[:i], p.TranscriptInputs[i+1:]...)
				break
			}
			for i := range p.Turns {
				if p.Turns[i].MessageID == id {
					p.Turns[i].MessageID = ""
				}
			}
		}
	}
}

func noteTranscriptInput(p *Projection, turnID string, m provider.Message) {
	// A repair may restore an identity outside any active turn. Retain the
	// original ownership across retraction/checkpoints rather than attaching
	// it to the repair's empty (or unrelated) turn.
	if original, ok := p.RetractedInputs[m.ID]; ok && m.Role == provider.RoleUser {
		turnID = original
		delete(p.RetractedInputs, m.ID)
	}
	for i := range p.TranscriptInputs {
		if p.TranscriptInputs[i].ID == m.ID {
			p.TranscriptInputs[i].Preview = catalogMessagePreview(m)
			return
		}
	}
	if m.Role != provider.RoleUser {
		return
	}
	p.TranscriptInputs = append(p.TranscriptInputs, transcriptInput{ID: m.ID, TurnID: turnID, Preview: catalogMessagePreview(m)})
	delete(p.HiddenTurns, turnID)
}

func visibleBoundaryCount(p Projection, endedOnly bool) int {
	count := 0
	for _, t := range p.Turns {
		if !p.HiddenTurns[t.TurnID] && (!endedOnly || t.EndSequence != 0) {
			count++
		}
	}
	return count
}
