package session

import (
	"encoding/json"
	"reflect"
	"testing"

	"reasonix/internal/provider"
)

func TestCatalogReducerRetractionRestoreAndAuthoredPreview(t *testing.T) {
	var commits []Commit
	reducer := catalogReducer{}
	var sequence uint64
	apply := func(turnID string, events ...Event) {
		t.Helper()
		commit := Commit{TurnID: turnID, FirstSequence: sequence + 1, EventCount: len(events), Events: events}
		for i := range commit.Events {
			sequence++
			commit.Events[i].Sequence = sequence
		}
		commits = append(commits, commit)
		if err := reducer.apply(commit); err != nil {
			t.Fatal(err)
		}
		full, err := Project(commits)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := reducer.metadata(Manifest{}), metadataFromProjection(Manifest{}, sequence, full); !reflect.DeepEqual(got, want) {
			t.Fatalf("reducer=%+v canonical=%+v", got, want)
		}
		if len(reducer.state.Messages)+len(reducer.state.ModelMessages)+len(reducer.state.Turns) != 0 {
			t.Fatal("catalog retained transcript bodies or full turn boundaries")
		}
	}
	message := func(kind, id, raw string) Event {
		payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, RawContent: raw, Content: "<context>provider-only</context>"}})
		return Event{Kind: kind, Payload: payload}
	}
	check := func(turns int, preview string) {
		t.Helper()
		got := reducer.metadata(Manifest{})
		if got.Turns != turns || got.Preview != preview {
			t.Fatalf("turns=%d preview=%q; want %d %q", got.Turns, got.Preview, turns, preview)
		}
	}
	for _, id := range []string{"first", "second"} {
		apply(id+"-turn", Event{Kind: "turn/start", Payload: json.RawMessage(`{}`)}, message("message/complete", id, id+" question"), Event{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)})
	}
	check(2, "first question")
	apply("", Event{Kind: "message/retract", Payload: json.RawMessage(`{"messageIds":["first"]}`)})
	check(1, "second question")
	apply("", message("message/upsert", "first", "restored question"))
	check(2, "second question")
	apply("", Event{Kind: "message/retract", Payload: json.RawMessage(`{"messageIds":["second"]}`)})
	check(1, "restored question")
	apply("", Event{Kind: "message/upsert", Payload: json.RawMessage(`{"message":{"id":"first","role":"assistant","content":"not a user preview"}}`)})
	check(1, "")
}
