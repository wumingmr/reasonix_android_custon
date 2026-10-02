package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"reasonix/internal/provider"
)

type messageRetractPayload struct {
	MessageIDs []string `json:"messageIds"`
	Reason     string   `json:"reason,omitempty"`
}

func retractedMessageIDs(event Event, payload json.RawMessage) ([]string, error) {
	var body messageRetractPayload
	if err := strictPayload(payload, &body); err != nil {
		return nil, damagedPayload(event, err)
	}
	if len(body.MessageIDs) == 0 {
		return nil, damagedPayload(event, fmt.Errorf("empty messageIds"))
	}
	seen := make(map[string]bool, len(body.MessageIDs))
	for _, id := range body.MessageIDs {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || seen[id] {
			return nil, damagedPayload(event, fmt.Errorf("invalid or duplicate message id"))
		}
		seen[id] = true
	}
	return body.MessageIDs, nil
}

// Execution, recovery, history and search must decode the same durable event
// schema. Projection-specific DTOs used to reject valid rewrite/import metadata.
type historyReplacePayload struct {
	Messages []provider.Message `json:"messages"`
	Reason   string             `json:"reason,omitempty"`
	Sources  []uint64           `json:"sourceSequences,omitempty"`
}

type legacyImportPayload struct {
	Source        Source             `json:"source"`
	Messages      []provider.Message `json:"messages"`
	Goal          json.RawMessage    `json:"goal,omitempty"`
	ModelRef      string             `json:"modelRef,omitempty"`
	ModelIdentity string             `json:"modelIdentity,omitempty"`
}

// ErrDuplicateMessageID refuses a write that would give two messages one
// stable id; every reader of the log keys a message by that id.
var ErrDuplicateMessageID = errors.New("session: duplicate stable message id")

// replacementEventMessages keeps the first occurrence of each id, so a log
// written before the writer refused duplicates projects and indexes alike.
func replacementEventMessages(event Event, payload json.RawMessage) ([]provider.Message, error) {
	messages, err := decodeReplacementMessages(event, payload)
	if err != nil {
		return nil, err
	}
	return firstMessageOccurrences(messages), nil
}

func decodeReplacementMessages(event Event, payload json.RawMessage) ([]provider.Message, error) {
	var messages []provider.Message
	var err error
	if event.Kind == "legacy/import" {
		var body legacyImportPayload
		err = strictPayload(payload, &body)
		messages = body.Messages
	} else {
		var body historyReplacePayload
		err = strictPayload(payload, &body)
		messages = body.Messages
	}
	if err != nil || messages == nil {
		return nil, damagedPayload(event, err)
	}
	return messages, nil
}

func firstMessageOccurrences(messages []provider.Message) []provider.Message {
	if _, duplicated := duplicateMessageID(messages); !duplicated {
		return messages
	}
	seen := make(map[string]bool, len(messages))
	unique := make([]provider.Message, 0, len(messages))
	for _, message := range messages {
		if message.ID != "" && seen[message.ID] {
			continue
		}
		seen[message.ID] = true
		unique = append(unique, message)
	}
	return unique
}

func duplicateMessageID(messages []provider.Message) (string, bool) {
	seen := make(map[string]bool, len(messages))
	for _, message := range messages {
		if message.ID == "" {
			continue
		}
		if seen[message.ID] {
			return message.ID, true
		}
		seen[message.ID] = true
	}
	return "", false
}

// checkReplacementIdentities refuses a new replacement that repeats an id. A
// payload that does not decode is the projection's to refuse.
func checkReplacementIdentities(event Event) error {
	if event.Kind != "history/replace" && event.Kind != "legacy/import" {
		return nil
	}
	messages, err := decodeReplacementMessages(event, event.Payload)
	if err != nil {
		return nil
	}
	if id, duplicated := duplicateMessageID(messages); duplicated {
		return fmt.Errorf("%w: %s message %q", ErrDuplicateMessageID, event.Kind, id)
	}
	return nil
}
