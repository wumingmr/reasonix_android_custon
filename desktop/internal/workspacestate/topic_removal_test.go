package workspacestate

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestTopicRemovalJournalSurvivesFailedEffectAndClonesEvidence(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "state.json"))
	failed := errors.New("external metadata unavailable")
	err := s.TransitionTopicRemoval(t.Context(), "remove", func(_ State, item *TopicRemoval, checkpoint func() error) error {
		*item = TopicRemoval{ID: "remove", TopicID: "topic", WorkspaceID: GlobalWorkspaceID, Phase: "prepared", Snapshot: json.RawMessage(`{"title":"keep"}`), Metadata: json.RawMessage(`{"future":[1,2]}`)}
		if err := checkpoint(); err != nil {
			return err
		}
		item.Phase = "committed"
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	state, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.TopicRemovals["remove"].Phase != "prepared" {
		t.Fatal("failure reported as durable commit")
	}
	state.TopicRemovals["remove"].Snapshot[0] = '!'
	state.TopicRemovals["remove"].Metadata[0] = '!'
	again, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(again.TopicRemovals["remove"].Snapshot) != `{"title":"keep"}` || string(again.TopicRemovals["remove"].Metadata) != `{"future":[1,2]}` {
		t.Fatal("snapshot aliases private reader cache")
	}
	before := again.Generation
	if err := s.TransitionTopicRemoval(t.Context(), "remove", func(_ State, item *TopicRemoval, _ func() error) error { item.Phase = "committed"; return nil }); err != nil {
		t.Fatal(err)
	}
	again, _ = s.Load(t.Context())
	if again.Generation <= before {
		t.Fatal("completion did not invalidate list cursors")
	}
}

func TestTopicRemovalUnknownFieldsRoundTrip(t *testing.T) {
	var item TopicRemoval
	if err := json.Unmarshal([]byte(`{"id":"remove","phase":"prepared","future":{"keep":[1,2]}}`), &item); err != nil {
		t.Fatal(err)
	}
	item.Phase = "committed"
	body, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["future"]) != `{"keep":[1,2]}` {
		t.Fatal("future writer evidence lost")
	}
	state := newState()
	body, _ = json.Marshal(state)
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["topicRemovals"]; exists {
		t.Fatal("empty collection changed old-data shape")
	}
}
