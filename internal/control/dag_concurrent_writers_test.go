package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/store"
	"reasonix/internal/tool"
)

// TestConcurrentControllersShareOneLogWithoutRecoveryCopies covers the short
// in-process overlap used by runtime replacement. The durable v3 history stays
// linear; it no longer manufactures same-log heads for each controller.
func TestConcurrentControllersShareOneLogWithoutRecoveryCopies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.jsonl")
	const systemPrompt = "SYS"
	reply := [][]provider.Chunk{{{Type: provider.ChunkText, Text: "ok"}, {Type: provider.ChunkDone}}}

	provA := &recordingProvider{streams: reply}
	execA := agent.New(provA, tool.NewRegistry(), agent.NewSession(systemPrompt), agent.Options{}, event.Discard)
	ctrlA := newOwnedTestController(t, Options{Runner: execA, Executor: execA, SystemPrompt: systemPrompt, SessionDir: dir, SessionPath: path, Label: "a", Sink: event.Discard})
	if err := ctrlA.RunTurn(context.Background(), "first from A"); err != nil {
		t.Fatalf("A first turn: %v", err)
	}
	if err := ctrlA.Snapshot(); err != nil {
		t.Fatalf("A snapshot: %v", err)
	}

	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	provB := &recordingProvider{streams: reply}
	execB := agent.New(provB, tool.NewRegistry(), agent.NewSession(systemPrompt), agent.Options{}, event.Discard)
	ctrlB := newOwnedTestController(t, Options{Runner: execB, Executor: execB, SystemPrompt: systemPrompt, SessionDir: dir, SessionPath: path, Label: "b", Sink: event.Discard})
	ctrlB.Resume(loaded, path)

	if err := ctrlA.RunTurn(context.Background(), "second from A"); err != nil {
		t.Fatalf("A second turn: %v", err)
	}
	if err := ctrlB.RunTurn(context.Background(), "second from B"); err != nil {
		t.Fatalf("B turn: %v", err)
	}
	if err := ctrlA.Snapshot(); err != nil {
		t.Fatalf("A snapshot: %v", err)
	}
	if err := ctrlB.Snapshot(); err != nil {
		t.Fatalf("B snapshot: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if store.IsSessionTranscriptName(entry.Name()) && entry.Name() != "shared.jsonl" {
			t.Fatalf("concurrent controllers created a transcript copy: %s", entry.Name())
		}
	}
	commits, err := session.Replay(sessionDirectory(path), nil)
	if err != nil {
		t.Fatalf("Replay v3: %v", err)
	}
	turnEnds := 0
	var kinds []string
	for _, commit := range commits {
		for _, event := range commit.Events {
			kinds = append(kinds, event.Kind)
			if event.Kind == "turn/end" {
				turnEnds++
			}
		}
	}
	if turnEnds != 3 {
		t.Fatalf("linear v3 turn endings = %d, want 3; kinds=%v", turnEnds, kinds)
	}
	if ctrlA.SessionPath() != path || ctrlB.SessionPath() != path {
		t.Fatalf("controllers moved off the shared path: %q %q", ctrlA.SessionPath(), ctrlB.SessionPath())
	}
	// system + three complete user/assistant turns come from the shared typed
	// event projection for both short-lived controller generations.
	if len(ctrlB.History()) != 7 || len(ctrlA.History()) != 7 {
		t.Fatalf("histories A=%d B=%d", len(ctrlA.History()), len(ctrlB.History()))
	}
}
