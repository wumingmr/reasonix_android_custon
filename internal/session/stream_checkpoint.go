package session

import (
	"encoding/json"

	"reasonix/internal/provider"
)

// StreamCheckpointKind carries the output a still-open stream has produced,
// as the local-only record its interruption would leave. It is optional so a
// reader that does not know it skips it; it enters history only when restart
// recovery closes the turn it belongs to.
const StreamCheckpointKind = "stream/checkpoint"

// StreamCheckpointEvent builds the event for one open-stream checkpoint.
func StreamCheckpointEvent(record provider.Message) (Event, error) {
	payload, err := json.Marshal(map[string]any{"message": record})
	if err != nil {
		return Event{}, err
	}
	return Event{Kind: StreamCheckpointKind, Optional: true, Payload: payload}, nil
}

// projectStreamCheckpoint keeps the newest checkpoint of the open turn. A
// payload this reader cannot decode is skipped like any unknown optional event.
func projectStreamCheckpoint(projection *Projection, ev Event) {
	var body struct {
		Message *provider.Message `json:"message"`
	}
	if projection.TurnID == "" || json.Unmarshal(ev.Payload, &body) != nil || body.Message == nil || body.Message.ID == "" || !body.Message.LocalOnly {
		return
	}
	projection.StreamCheckpoint = body.Message
}

// supersedeStreamCheckpoint drops the checkpoint once anything after it
// commits a message or closes the turn: the stream it described has either
// become a durable message or been recorded as interrupted.
func supersedeStreamCheckpoint(projection *Projection, kind string) {
	switch kind {
	case "message/complete", "message/upsert", "message/retract", "history/replace", "legacy/import", "turn/start", "turn/end":
		projection.StreamCheckpoint = nil
	}
}

// streamCheckpointRecoveryEvents materialises the open turn's checkpoint as
// the interrupted record a cancelled stream would have committed.
func streamCheckpointRecoveryEvents(projection Projection) []Event {
	if projection.StreamCheckpoint == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"message": projection.StreamCheckpoint})
	if err != nil {
		return nil
	}
	return []Event{{Kind: "message/upsert", Payload: payload}}
}
