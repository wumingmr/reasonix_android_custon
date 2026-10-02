package session

import (
	"bytes"
	"encoding/json"
	"reasonix/internal/provider"
	"reflect"
	"testing"
)

func TestClosureEventsEvidenceOrderingAndIdempotence(t *testing.T) {
	event := func(kind, payload string) Event { return Event{Kind: kind, Payload: json.RawMessage(payload)} }
	initial := Commit{Events: []Event{
		event("tool/call", `{"id":"z-unstarted","name":"lookup"}`),
		event("tool/call", `{"id":"a-started","name":"write"}`),
		event("tool/start", `{"id":"a-started","name":"write"}`),
		event("tool/call", `{"id":"done","name":"read"}`),
		event("tool/start", `{"id":"done","name":"read"}`),
		event("tool/result", `{"id":"done","name":"read","runState":"completed"}`),
		event("interaction/created", `{"id":"z-interaction"}`),
		event("interaction/created", `{"id":"a-interaction"}`),
		event("step/start", `{"id":"z-step"}`),
		event("step/start", `{"id":"a-step"}`),
		event("step/start", `{"id":"done-step"}`),
		event("step/end", `{"id":"done-step"}`),
	}}
	projection, err := Project([]Commit{initial})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(projection)
	closures := ClosureEvents(projection, "unavailable", "runtime exited")
	after, _ := json.Marshal(projection)
	if !bytes.Equal(before, after) {
		t.Fatal("closure planner mutated input projection")
	}
	var got []string
	for _, e := range closures {
		var body struct {
			ID       string                `json:"id"`
			RunState provider.ToolRunState `json:"runState"`
			State    string                `json:"state"`
		}
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Kind+":"+body.ID)
		switch body.ID {
		case "a-started":
			if body.RunState != provider.ToolRunUnknown {
				t.Fatalf("started tool state=%q", body.RunState)
			}
		case "z-unstarted":
			if body.RunState != provider.ToolRunNotStarted {
				t.Fatalf("unstarted tool state=%q", body.RunState)
			}
		}
		if e.Kind == "interaction/resolved" && body.State != "unavailable" {
			t.Fatalf("interaction state=%q", body.State)
		}
	}
	want := []string{"tool/result:a-started", "tool/result:z-unstarted", "interaction/resolved:a-interaction", "interaction/resolved:z-interaction", "step/end:a-step", "step/end:z-step"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("closures=%v, want=%v", got, want)
	}
	second := ClosureEvents(projection, "unavailable", "runtime exited")
	if !reflect.DeepEqual(second, closures) {
		t.Fatal("repeated plan changed ordering or payload")
	}
	closed, err := Project([]Commit{initial, {Events: closures}})
	if err != nil {
		t.Fatal(err)
	}
	if again := ClosureEvents(closed, "unavailable", "runtime exited"); len(again) != 0 {
		t.Fatalf("closed authority generated duplicate closures: %+v", again)
	}
	if got := ClosureEvents(Projection{}, "unavailable", "runtime exited"); len(got) != 0 {
		t.Fatalf("empty projection invented authority: %+v", got)
	}
}
