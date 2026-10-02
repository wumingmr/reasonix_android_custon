package control

import (
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
	"reasonix/internal/transcript"
)

func TestTranscriptRetainsProtocolRecoveryAfterProviderFailure(t *testing.T) {
	p := &manualProtocolProvider{MockProvider: testutil.NewMock("strict", testutil.ErrorTurn(&provider.APIError{Status: 400, Body: `{"model":"deepseek"}`}))}
	session := agent.NewSession("system")
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: "earlier", ReasoningContent: "proof"})
	a := agent.New(p, tool.NewRegistry(), session, agent.Options{}, event.Discard)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	done := make(chan event.Event, 1)
	c := newOwnedTestController(t, Options{Runner: a, Executor: a, SessionDir: dir, SessionPath: path, Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
	})})
	defer c.Close()
	c.Send("next")
	select {
	case e := <-done:
		if e.Err == nil || e.ProtocolRecovery == nil {
			t.Fatal("expected recoverable provider failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not settle")
	}
	action := a.PendingProtocolRecovery()
	if action == nil {
		t.Fatal("missing recovery token")
	}
	check := func(rows []transcript.Message) {
		t.Helper()
		for _, row := range rows {
			if row.ProtocolRecovery != nil && row.ProtocolRecovery.ID == action.ID && row.Pending {
				return
			}
		}
		t.Fatal("authoritative display lost the pending recovery action")
	}
	snap, err := c.TranscriptSnapshot(transcript.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var rows []transcript.Message
	for _, r := range snap.Records {
		rows = append(rows, r.Message)
	}
	check(rows)
	checkpoint, exists, err := transcript.LoadCheckpoint(path)
	if err != nil || !exists {
		t.Fatalf("checkpoint: %v", err)
	}
	check(checkpoint.Records)
	if checkpoint.CoveredThroughSeq != snap.CoveredThroughSeq {
		t.Fatal("checkpoint coverage drifted")
	}
}
