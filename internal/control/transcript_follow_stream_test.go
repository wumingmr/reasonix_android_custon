package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
	"reasonix/internal/transcript"
)

type followTally struct {
	polls, changes, maxBatch   int
	resets, gaps, changeResets int
}

// desktopFollower applies transcriptFollowClient's acceptance rules: a
// response reset, a change reset or a skipped revision each cost a baseline.
type desktopFollower struct {
	c            *Controller
	subscription string
	revision     uint64
	tally        followTally
}

func (f *desktopFollower) baseline(ctx context.Context) error {
	response, err := f.c.TranscriptFollow(ctx, transcript.FollowRequest{})
	if err == nil && response.Snapshot == nil {
		err = errors.New("baseline without snapshot")
	}
	if err != nil {
		return err
	}
	f.subscription, f.revision = response.Subscription, response.Snapshot.ProjectionRevision
	return nil
}

// run polls until done is closed and the queue is drained; work stands in for
// the renderer applying one delivered batch before it asks for the next.
func (f *desktopFollower) run(ctx context.Context, done <-chan struct{}, work time.Duration) error {
	finished := false
	for {
		select {
		case <-done:
			finished = true
		default:
		}
		pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		response, err := f.c.TranscriptFollow(pollCtx, transcript.FollowRequest{Subscription: f.subscription, AfterRevision: f.revision})
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		f.tally.polls++
		f.tally.changes += len(response.Changes)
		f.tally.maxBatch = max(f.tally.maxBatch, len(response.Changes))
		broken := response.ResetRequired
		if broken {
			f.tally.resets++
		}
		for _, change := range response.Changes {
			if broken || change.Revision <= f.revision {
				continue
			}
			switch {
			case change.ResetRequired:
				f.tally.changeResets++
				broken = true
			case change.Revision != f.revision+1:
				f.tally.gaps++
				broken = true
			default:
				f.revision = change.Revision
			}
		}
		if broken {
			_, _ = f.c.TranscriptFollow(context.Background(), transcript.FollowRequest{Subscription: f.subscription, Close: true})
			if err := f.baseline(ctx); err != nil {
				return err
			}
			continue
		}
		if finished && len(response.Changes) == 0 {
			_, _ = f.c.TranscriptFollow(context.Background(), transcript.FollowRequest{Subscription: f.subscription, Close: true})
			return nil
		}
		time.Sleep(work)
	}
}

// shellFloodTool writes output the way a verbose build does: many short
// lines, each reaching the agent as its own progress chunk.
type shellFloodTool struct{ lines int }

func (shellFloodTool) Name() string            { return "flood" }
func (shellFloodTool) Description() string     { return "prints build output" }
func (shellFloodTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (shellFloodTool) ReadOnly() bool          { return true }
func (f shellFloodTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	emit, _ := tool.ProgressFrom(ctx)
	for i := range f.lines {
		if emit != nil {
			emit(fmt.Sprintf("compiling unit %d\n", i))
		}
		if i%2 == 1 {
			time.Sleep(time.Millisecond)
		}
	}
	return "ok", nil
}

func TestTranscriptFollowSteadyStreamingNeverResetsFollower(t *testing.T) {
	chunks := make([]provider.Chunk, 0, 3001)
	for range 1500 {
		chunks = append(chunks, provider.Chunk{Type: provider.ChunkReasoning, Text: "think "})
	}
	for range 1500 {
		chunks = append(chunks, provider.Chunk{Type: provider.ChunkText, Text: "word "})
	}
	chunks = append(chunks, provider.Chunk{Type: provider.ChunkDone})
	flood := []provider.ToolCall{{ID: "call-flood", Name: "flood", Arguments: "{}"}}
	cases := map[string][]testutil.Turn{
		"reasoning and text": {{Chunks: chunks}},
		"shell output":       {{ToolCalls: flood}, {Text: "built"}},
	}
	for name, turns := range cases {
		t.Run(name, func(t *testing.T) {
			service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "transcript-follow"})
			if err != nil {
				t.Fatal(err)
			}
			registry := tool.NewRegistry()
			registry.Add(shellFloodTool{lines: 3000})
			executor := agent.New(testutil.NewMock("test", turns...), registry, agent.NewSession("system"), agent.Options{}, event.Discard)
			c := newOwnedTestController(t, Options{Runner: executor, Executor: executor, Sink: event.Discard,
				SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})

			follower := &desktopFollower{c: c}
			if err := follower.baseline(t.Context()); err != nil {
				t.Fatal(err)
			}
			done, result := make(chan struct{}), make(chan error, 1)
			go func() { result <- follower.run(t.Context(), done, 400*time.Millisecond) }()
			started := time.Now()
			if err := c.RunTurn(t.Context(), "question"); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(started)
			close(done)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			tally := follower.tally
			t.Logf("turn=%v polls=%d changes=%d maxBatch=%d resets=%d gaps=%d changeResets=%d",
				elapsed, tally.polls, tally.changes, tally.maxBatch, tally.resets, tally.gaps, tally.changeResets)
			if tally.resets+tally.gaps+tally.changeResets != 0 {
				t.Fatalf("steady output forced %d follower baselines: %+v", tally.resets+tally.gaps+tally.changeResets, tally)
			}
		})
	}
}
