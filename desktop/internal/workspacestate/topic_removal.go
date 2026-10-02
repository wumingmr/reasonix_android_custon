package workspacestate

import (
	"context"
	"encoding/json"

	"reasonix/internal/fileutil"
)

// TopicRemoval journals metadata-only removals independently of session
// operations. Previous registry readers preserve this optional collection.
type TopicRemoval struct {
	ID              string          `json:"id"`
	WorkspaceID     string          `json:"workspaceId"`
	TopicID         string          `json:"topicId"`
	Token           string          `json:"token"`
	SessionToken    string          `json:"sessionToken,omitempty"`
	Disposition     string          `json:"disposition"`
	Phase           string          `json:"phase"`
	ArchivedAt      int64           `json:"archivedAt"`
	Snapshot        json.RawMessage `json:"snapshot,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	RestoreRevision int64           `json:"restoreRevision,omitempty"`
	extra           map[string]json.RawMessage
}

// TransitionTopicRemoval holds the registry's cross-process writer lock across
// validation and external metadata writes. checkpoint durably saves intent
// BEFORE an external effect; errors retain that intent for a fenced retry.
// Neither callback may call this Store. Callers follow registry -> projects ->
// topic-store lock order inside the transaction.
func (s *Store) TransitionTopicRemoval(ctx context.Context, id string, transaction func(State, *TopicRemoval, func() error) error) error {
	return s.mutate(ctx, func(state *State) error {
		item := state.TopicRemovals[id]
		checkpoint := func() error {
			state.TopicRemovals[id] = item
			state.Generation++
			state.Initialized = true
			if err := validate(*state); err != nil {
				return err
			}
			body, err := json.Marshal(state)
			if err != nil {
				return err
			}
			return fileutil.AtomicWriteFileStrict(s.path, append(body, '\n'), 0o600)
		}
		if err := transaction(*state, &item, checkpoint); err != nil {
			return err
		}
		state.TopicRemovals[id] = item
		return nil
	})
}

func (r *TopicRemoval) UnmarshalJSON(body []byte) error {
	type plain TopicRemoval
	var decoded plain
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	for _, key := range []string{"id", "workspaceId", "topicId", "token", "sessionToken", "disposition", "phase", "archivedAt", "snapshot", "metadata", "restoreRevision"} {
		delete(fields, key)
	}
	*r = TopicRemoval(decoded)
	r.extra = fields
	return nil
}

func (r TopicRemoval) MarshalJSON() ([]byte, error) {
	type plain TopicRemoval
	body, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	return mergeUnknown(body, r.extra)
}
