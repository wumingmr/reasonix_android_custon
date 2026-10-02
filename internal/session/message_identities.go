package session

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// messageIdentities is every stable message id the log holds live. The writer
// needs it because a Service-owned projection drops durable message bodies, so
// the projection alone cannot see an id claimed before its last checkpoint.
type messageIdentities map[string]struct{}

func identitiesOf(ids []string) messageIdentities {
	set := make(messageIdentities, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func (ids messageIdentities) holds(id string) bool {
	_, ok := ids[id]
	return ok
}

func (ids messageIdentities) list() []string {
	return slices.Collect(maps.Keys(ids))
}

// identityChange is one commit's effect on the live set, kept apart so a
// refused commit leaves the set untouched.
type identityChange struct {
	reset bool
	live  map[string]bool
	// duplicate is the first message/complete id the commit repeats.
	duplicate string
}

func (c *identityChange) has(ids messageIdentities, id string) bool {
	if live, ok := c.live[id]; ok {
		return live
	}
	if c.reset {
		return false
	}
	_, ok := ids[id]
	return ok
}

// changeFor mirrors the projection's message semantics. A payload it cannot
// decode is left for the projection to refuse.
func (ids messageIdentities) changeFor(commit Commit) identityChange {
	change := identityChange{live: map[string]bool{}}
	for _, event := range commit.Events {
		switch event.Kind {
		case "message/complete", "message/upsert":
			id := eventMessageID(event)
			if id == "" {
				continue
			}
			if event.Kind == "message/complete" && change.has(ids, id) {
				if change.duplicate == "" {
					change.duplicate = id
				}
				continue
			}
			change.live[id] = true
		case "message/retract":
			retracted, err := retractedMessageIDs(event, event.Payload)
			if err != nil {
				continue
			}
			for _, id := range retracted {
				change.live[id] = false
			}
		case "history/replace", "legacy/import":
			messages, err := replacementEventMessages(event, event.Payload)
			if err != nil {
				continue
			}
			change.reset, change.live = true, map[string]bool{}
			for _, message := range messages {
				if message.ID != "" {
					change.live[message.ID] = true
				}
			}
		}
	}
	return change
}

func (ids *messageIdentities) apply(change identityChange) {
	if change.reset || *ids == nil {
		*ids = messageIdentities{}
	}
	for id, live := range change.live {
		if live {
			(*ids)[id] = struct{}{}
		} else {
			delete(*ids, id)
		}
	}
}

// admit records a commit a reader replays; a repeated message/complete keeps
// the id's first occurrence, as the projection does.
func (ids *messageIdentities) admit(commit Commit) {
	ids.apply(ids.changeFor(commit))
}

// admitEvent reports whether a replayed event is kept, recording in repeated
// the sequence of a message/complete whose id is already live.
func (ids *messageIdentities) admitEvent(event Event, repeated map[uint64]bool) bool {
	if event.Kind == "message/complete" && ids.holds(eventMessageID(event)) {
		repeated[event.Sequence] = true
		return false
	}
	ids.admit(Commit{Events: []Event{event}})
	return true
}

func eventMessageID(event Event) string {
	var body struct {
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if json.Unmarshal(event.Payload, &body) != nil {
		return ""
	}
	return body.Message.ID
}

func duplicateMessageError(id string) error {
	return fmt.Errorf("%w: message/complete %q", ErrDuplicateMessageID, id)
}
