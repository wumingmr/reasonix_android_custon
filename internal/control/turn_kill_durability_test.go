package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

// killMidStreamProvider answers the first request, then streams part of the
// second reply and ends the process the way a force-quit does: no deferred
// cleanup, no TurnDone, no final save.
type killMidStreamProvider struct {
	calls  int
	exitIn time.Duration
}

func (*killMidStreamProvider) Name() string { return "kill-mid-stream" }

func (p *killMidStreamProvider) Stream(context.Context, provider.Request) (<-chan provider.Chunk, error) {
	p.calls++
	ch := make(chan provider.Chunk, 4)
	if p.calls == 1 {
		ch <- provider.Chunk{Type: provider.ChunkText, Text: "first answer"}
		ch <- provider.Chunk{Type: provider.ChunkDone}
		close(ch)
		return ch, nil
	}
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "partial reply"}
	go func() {
		time.Sleep(p.exitIn)
		os.Exit(73)
	}()
	return ch, nil
}

func runKillFixture(t *testing.T, name, envKey string) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$")
	cmd.Env = append(os.Environ(), envKey+"="+root)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("kill fixture: %v %s", err, out)
	}
	return root
}

func containsUserText(messages []provider.Message, text string) bool {
	for _, m := range messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

func TestKilledLegacyTurnKeepsItsPrompt(t *testing.T) {
	if root := os.Getenv("REASONIX_LEGACY_TURN_KILL_FIXTURE"); root != "" {
		a := agent.New(&killMidStreamProvider{exitIn: 300 * time.Millisecond}, tool.NewRegistry(), agent.NewSession("sys"), agent.Options{}, event.Discard)
		c := newOwnedTestController(t, Options{Executor: a, Runner: a, SessionPath: filepath.Join(root, "session.jsonl"), SessionDir: root, Sink: event.Discard, NativeLegacySession: true})
		if err := c.RunTurn(context.Background(), "first question"); err != nil {
			t.Fatal(err)
		}
		_ = c.RunTurn(context.Background(), "second question")
		t.Fatal("fixture was not killed")
	}
	root := runKillFixture(t, "TestKilledLegacyTurnKeepsItsPrompt", "REASONIX_LEGACY_TURN_KILL_FIXTURE")
	path := filepath.Join(root, "session.jsonl")
	sess, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	a := agent.New(nil, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: a, SessionPath: path, SessionDir: root, Sink: event.Discard, NativeLegacySession: true})
	c.recoverInterruptedTurn(path)
	history := c.History()
	if !containsUserText(history, "first question") {
		t.Fatalf("completed turn lost after restart:\n%s", requestMessagesText(history))
	}
	if !containsUserText(history, "second question") {
		t.Fatalf("prompt of the killed turn lost after restart:\n%s", requestMessagesText(history))
	}
}

func openKilledV3Session(t *testing.T, root string) (*session.Service, *session.ClientBinding, session.SessionRef) {
	t.Helper()
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	ref := session.SessionRef{HostID: "desktop", SessionID: "killed"}
	binding, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Release(context.Background()) })
	return service, binding, ref
}

func readNewestHistory(t *testing.T, service *session.Service, ref session.SessionRef) session.HistoryWindowPage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, err := service.Query().ReadHistoryWindow(t.Context(), ref, session.HistoryWindowRequest{Anchor: "newest", Limit: 64})
		if err != nil {
			t.Fatal(err)
		}
		if page.Status != "preparing" {
			return page
		}
		if time.Now().After(deadline) {
			t.Fatalf("history window still preparing")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func historyRowsContaining(page session.HistoryWindowPage, text string) []session.PersistentMessage {
	var rows []session.PersistentMessage
	for _, m := range page.Messages {
		if strings.Contains(string(m.Inline)+m.Preview, text) {
			rows = append(rows, m)
		}
	}
	return rows
}

func TestKilledTurnKeepsItsPartialOutput(t *testing.T) {
	if root := os.Getenv("REASONIX_V3_TURN_KILL_FIXTURE"); root != "" {
		midTurnSnapshotInterval.Store(int64(25 * time.Millisecond))
		service, err := session.NewService("desktop", session.NewFilesystemPersistence(root))
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "killed"})
		if err != nil {
			t.Fatal(err)
		}
		a := agent.New(&killMidStreamProvider{exitIn: time.Second}, tool.NewRegistry(), agent.NewSession("sys"), agent.Options{}, event.Discard)
		c := newOwnedTestController(t, Options{Runner: a, Executor: a, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
		if err := c.RunTurn(context.Background(), "first question"); err != nil {
			t.Fatal(err)
		}
		c.Send("second question")
		time.Sleep(10 * time.Second)
		t.Fatal("fixture was not killed")
	}
	root := runKillFixture(t, "TestKilledTurnKeepsItsPartialOutput", "REASONIX_V3_TURN_KILL_FIXTURE")
	service, binding, ref := openKilledV3Session(t, root)
	page := readNewestHistory(t, service, ref)
	if len(historyRowsContaining(page, "second question")) != 1 {
		t.Fatalf("prompt of the killed turn not durable: %+v", page.Messages)
	}
	partial := historyRowsContaining(page, "partial reply")
	if len(partial) != 1 {
		t.Fatalf("partial output of the killed turn not durable after reopen: %d rows", len(partial))
	}
	var record provider.Message
	if err := json.Unmarshal(partial[0].Inline, &record); err != nil {
		t.Fatalf("partial row is not an inline message: %v", err)
	}
	if !record.LocalOnly || record.InterruptedTurn == nil {
		t.Fatalf("partial output must be the local-only interrupted record: %+v", record)
	}
	a := agent.New(nil, tool.NewRegistry(), agent.NewSession("sys"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: a, Runner: a, Sink: event.Discard, SessionService: service, SessionRuntime: binding.Runtime(), ExclusiveSession: true})
	history := c.History()
	if !containsUserText(history, "second question") {
		t.Fatalf("prompt of the killed turn missing from model history:\n%s", requestMessagesText(history))
	}
	for _, m := range provider.ModelMessages(history) {
		if strings.Contains(m.Content, "partial reply") {
			t.Fatalf("partial output reached the provider-visible history: %+v", m)
		}
	}
}

// pausingStreamProvider streams the opening of a reply, holds the stream open
// long enough for several autosave ticks, then completes it.
type pausingStreamProvider struct{ hold time.Duration }

func (*pausingStreamProvider) Name() string { return "pausing-stream" }

func (p *pausingStreamProvider) Stream(context.Context, provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 4)
	go func() {
		defer close(ch)
		ch <- provider.Chunk{Type: provider.ChunkText, Text: "alpha "}
		time.Sleep(p.hold)
		ch <- provider.Chunk{Type: provider.ChunkText, Text: "omega"}
		ch <- provider.Chunk{Type: provider.ChunkDone}
	}()
	return ch, nil
}

func TestCompletedStreamSupersedesItsCheckpoint(t *testing.T) {
	old := midTurnSnapshotInterval.Load()
	midTurnSnapshotInterval.Store(int64(20 * time.Millisecond))
	t.Cleanup(func() { midTurnSnapshotInterval.Store(old) })
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- struct{}{}
		}
	})
	a := agent.New(&pausingStreamProvider{hold: 300 * time.Millisecond}, tool.NewRegistry(), agent.NewSession("sys"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Runner: a, Executor: a, Sink: sink, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	c.Send("question")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish")
	}
	c.autosaveWG.Wait()
	page, err := runtime.Session().AcceptedPage(t.Context(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	for _, commit := range page.Commits {
		for _, e := range commit.Events {
			if e.Kind == "stream/checkpoint" {
				checkpoints++
			}
		}
	}
	if checkpoints == 0 {
		t.Fatalf("no partial-output checkpoint was written while the stream was open")
	}
	history := readNewestHistory(t, service, runtime.Ref())
	rows := historyRowsContaining(history, "alpha")
	if len(rows) != 1 || !strings.Contains(string(rows[0].Inline)+rows[0].Preview, "omega") {
		t.Fatalf("completed reply must appear exactly once, as the completed message: %+v", rows)
	}
}

// committedThenKilledProvider completes a streamed message that asks for a
// tool, after holding the stream open across several autosave ticks.
type committedThenKilledProvider struct{ hold time.Duration }

func (*committedThenKilledProvider) Name() string { return "committed-then-killed" }

func (p *committedThenKilledProvider) Stream(context.Context, provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 4)
	go func() {
		defer close(ch)
		ch <- provider.Chunk{Type: provider.ChunkText, Text: "alpha "}
		time.Sleep(p.hold)
		ch <- provider.Chunk{Type: provider.ChunkText, Text: "omega"}
		ch <- provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "call-1", Name: "hang", Arguments: "{}"}}
		ch <- provider.Chunk{Type: provider.ChunkDone}
	}()
	return ch, nil
}

// killingTool ends the process while the turn that called it is still open.
type killingTool struct{}

func (killingTool) Name() string            { return "hang" }
func (killingTool) Description() string     { return "hangs" }
func (killingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (killingTool) ReadOnly() bool          { return true }
func (killingTool) Execute(context.Context, json.RawMessage) (string, error) {
	time.Sleep(300 * time.Millisecond)
	os.Exit(73)
	return "", nil
}

func TestKilledAfterCommittedStreamShowsItOnce(t *testing.T) {
	if root := os.Getenv("REASONIX_V3_COMMITTED_KILL_FIXTURE"); root != "" {
		midTurnSnapshotInterval.Store(int64(20 * time.Millisecond))
		service, err := session.NewService("desktop", session.NewFilesystemPersistence(root))
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "killed"})
		if err != nil {
			t.Fatal(err)
		}
		reg := tool.NewRegistry()
		reg.Add(killingTool{})
		a := agent.New(&committedThenKilledProvider{hold: 300 * time.Millisecond}, reg, agent.NewSession("sys"), agent.Options{}, event.Discard)
		c := newOwnedTestController(t, Options{Runner: a, Executor: a, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
		c.Send("question")
		time.Sleep(10 * time.Second)
		t.Fatal("fixture was not killed")
	}
	root := runKillFixture(t, "TestKilledAfterCommittedStreamShowsItOnce", "REASONIX_V3_COMMITTED_KILL_FIXTURE")
	service, binding, ref := openKilledV3Session(t, root)
	events, err := binding.Runtime().Session().AcceptedPage(t.Context(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	for _, commit := range events.Commits {
		for _, e := range commit.Events {
			if e.Kind == "stream/checkpoint" {
				checkpoints++
			}
		}
	}
	if checkpoints == 0 {
		t.Fatalf("no partial-output checkpoint was written while the stream was open")
	}
	rows := historyRowsContaining(readNewestHistory(t, service, ref), "alpha")
	if len(rows) != 1 {
		t.Fatalf("committed reply must appear exactly once after recovery, got %d rows", len(rows))
	}
	var message provider.Message
	if err := json.Unmarshal(rows[0].Inline, &message); err != nil {
		t.Fatal(err)
	}
	if message.LocalOnly || message.Role != provider.RoleAssistant || !strings.Contains(message.Content, "omega") {
		t.Fatalf("recovery replaced the committed reply with its checkpoint: %+v", message)
	}
}
