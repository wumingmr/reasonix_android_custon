package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

type terminationBlockingProvider struct{}

func (terminationBlockingProvider) Name() string { return "termination-test" }
func (terminationBlockingProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	chunks := make(chan provider.Chunk, 1)
	go func() {
		defer close(chunks)
		select {
		case chunks <- provider.Chunk{Type: provider.ChunkText, Text: "partial answer"}:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
		chunks <- provider.Chunk{Type: provider.ChunkError, Err: ctx.Err()}
	}()
	return chunks, nil
}

func terminationCommitHistory(t *testing.T, c *Controller) []session.Commit {
	t.Helper()
	store := c.sessionEventStore()
	if store == nil {
		t.Fatal("missing session store")
	}
	if _, err := store.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var commits []session.Commit
	var cursor uint64
	for {
		page, err := store.Handle().Read(t.Context(), cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		commits = append(commits, page.Commits...)
		if !page.Truncated {
			break
		}
		if page.Next == cursor {
			t.Fatal("event pagination stalled")
		}
		cursor = page.Next
	}
	return commits
}

func assertSingleTerminationCommit(t *testing.T, commits []session.Commit, turnID string, wantCleanup bool) {
	t.Helper()
	terminals, cleanups := 0, 0
	for _, commit := range commits {
		terminal, cleanup := false, false
		for _, e := range commit.Events {
			if e.Kind == "history/replace" {
				t.Fatal("pause emitted history/replace")
			}
			if commit.TurnID == turnID {
				terminal = terminal || e.Kind == "turn/end"
				cleanup = cleanup || e.Kind == "model/context-replace"
			}
		}
		if terminal {
			terminals++
			if commit.OperationID != "turn-finalize:"+turnID {
				t.Fatalf("unstable terminal operation %q", commit.OperationID)
			}
		}
		if cleanup {
			cleanups++
			if !terminal {
				t.Fatal("cleanup committed separately from turn/end")
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal commits=%d, want=1", terminals)
	}
	if wantCleanup && cleanups != 1 {
		t.Fatalf("cleanup commits=%d, want=1", cleanups)
	}
}

func TestTerminationCommitRealSendPartialCancel(t *testing.T) {
	done := make(chan event.Event, 8)
	text := make(chan struct{}, 1)
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
		if e.Kind == event.Text {
			select {
			case text <- struct{}{}:
			default:
			}
		}
	})
	exec := agent.New(terminationBlockingProvider{}, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, sink)
	dir := t.TempDir()
	c := newOwnedTestController(t, Options{Runner: exec, Executor: exec, Sink: sink, SessionDir: dir, SessionPath: filepath.Join(dir, "session.jsonl")})
	c.Send("keep my question")
	awaitPromptLedgerTest(t, text, "partial output")
	c.CancelSession()
	c.CancelSession()
	terminal := waitTurnDoneEvent(t, done)
	waitIdleAdmission(t, c)
	assertSingleTerminationCommit(t, terminationCommitHistory(t, c), terminal.TurnID, true)
	kept, recovery := false, false
	for _, m := range c.sessionEventStore().Snapshot().Projection.Messages {
		kept = kept || (m.Role == provider.RoleUser && m.Content == "keep my question")
		// Existing sampling cancellation discards speculative result.text before
		// recordInterruptedDisplay (sampling_recovery.go). Preserve that policy:
		// this path retains a pending marker, not uncommitted stream text.
		recovery = recovery || (m.LocalOnly && m.InterruptedTurn != nil && m.InterruptedTurn.Pending)
	}
	if !kept || !recovery {
		t.Fatalf("retained user=%v pending recovery=%v", kept, recovery)
	}
}

func TestTerminationCommitFallbackAndSynthetic(t *testing.T) {
	for _, name := range []string{"fallback", "synthetic", "persisted-partial"} {
		t.Run(name, func(t *testing.T) {
			synthetic := name == "synthetic"
			done := make(chan event.Event, 8)
			c, _, _ := exclusiveTestController(t, event.FuncSink(func(e event.Event) {
				if e.Kind == event.TurnDone {
					done <- e
				}
			}))
			started := make(chan struct{})
			question := provider.Message{ID: "current-user", Role: provider.RoleUser, Content: "preserve exact input"}
			c.runGuarded(func(ctx context.Context) error {
				start := c.executor.Session().Len()
				if synthetic {
					c.executor.Session().Add(provider.Message{ID: "synthetic", Role: provider.RoleUser, Content: "internal generated input"})
					if err := c.RecordSessionMessages(ctx, "synthetic-input", c.executor.Session().Snapshot()[start:]); err != nil {
						return err
					}
				}
				if name == "persisted-partial" {
					c.executor.Session().Add(question)
					c.executor.Session().Add(provider.Message{ID: "partial", Role: provider.RoleAssistant, Content: "committed fragment", ReasoningContent: "partial reasoning"})
					if err := c.RecordSessionMessages(ctx, "partial-input", c.executor.Session().Snapshot()[start:]); err != nil {
						return err
					}
				}
				close(started)
				<-ctx.Done()
				if synthetic {
					c.stripTurnMessagesAfter(start)
				} else {
					c.stripCancelledVisibleTurnMessagesAfterWithFallback(start, question)
				}
				return ctx.Err()
			})
			awaitPromptLedgerTest(t, started, "turn start")
			c.CancelSession()
			first := waitTurnDoneEvent(t, done)
			waitIdleAdmission(t, c)
			commits := terminationCommitHistory(t, c)
			assertSingleTerminationCommit(t, commits, first.TurnID, true)
			found, retracted := false, false
			for _, m := range c.sessionEventStore().Snapshot().Projection.Messages {
				if m.ID == question.ID {
					found = true
				}
				if m.ID == "synthetic" {
					t.Fatal("synthetic message survived cancellation")
				}
			}
			for _, commit := range commits {
				for _, e := range commit.Events {
					retracted = retracted || e.Kind == "message/retract"
				}
			}
			if synthetic && !retracted {
				t.Fatal("synthetic cancellation omitted retraction")
			}
			if !synthetic && !found {
				t.Fatal("pre-executor fallback lost")
			}
			if name == "persisted-partial" {
				local := false
				for _, m := range c.sessionEventStore().Snapshot().Projection.Messages {
					local = local || (m.ID == "partial" && m.LocalOnly && m.Content == "committed fragment" && m.ReasoningContent == "partial reasoning")
				}
				if !local {
					t.Fatal("persisted partial fragment lost during cleanup")
				}
			}
			if got := c.runGuarded(func(context.Context) error { return nil }); got != turnStarted {
				t.Fatalf("next admission=%v", got)
			}
			second := waitTurnDoneEvent(t, done)
			waitIdleAdmission(t, c)
			if second.TurnID == first.TurnID {
				t.Fatal("next turn reused terminated ID")
			}
			assertSingleTerminationCommit(t, terminationCommitHistory(t, c), first.TurnID, true)
		})
	}
}

func TestTerminationCommitWatchdogRejectsLateWorker(t *testing.T) {
	terminals := make(chan event.Event, 4)
	c, _, _ := exclusiveTestController(t, event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			terminals <- e
		}
	}))
	c.testCancelGrace = time.Nanosecond
	started, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	c.runGuarded(func(context.Context) error {
		close(started)
		<-release
		defer close(returned)
		c.sink.Emit(event.Event{Kind: event.ToolResult, Tool: event.Tool{ID: "late", Name: "todo_write", TodoWritten: true, Todos: []event.Todo{{Content: "late mutation", Status: "completed"}}}})
		return nil
	})
	awaitPromptLedgerTest(t, started, "uncooperative turn start")
	c.mu.Lock()
	idleDone := c.turns.finishingBound.idleDone
	c.mu.Unlock()
	c.CancelSession()
	// Terminal publication follows the durable commit and runtime publication.
	// Wait for that barrier: a five-second observer deadline also measures host
	// fsync latency, which is not this test's late-worker exclusion contract.
	terminal := <-terminals
	if state := c.PublishedRuntimeStateSnapshot(); state.Phase != "recovery_required" || terminal.Status != event.TurnRecoveryRequired {
		t.Fatalf("watchdog terminal did not publish recovery: terminal=%+v state=%+v", terminal, state)
	}
	before := terminationCommitHistory(t, c)
	var turnID string
	for _, commit := range before {
		for _, e := range commit.Events {
			if e.Kind == "turn/end" {
				turnID = commit.TurnID
			}
		}
	}
	if turnID == "" {
		t.Fatal("watchdog published recovery before terminal commit")
	}
	c.CancelSession()
	once.Do(func() { close(release) })
	awaitPromptLedgerTest(t, returned, "late worker body return")
	awaitPromptLedgerTest(t, idleDone, "sealed worker finalization")
	assertSingleTerminationCommit(t, terminationCommitHistory(t, c), turnID, false)
	if p := c.sessionEventStore().Snapshot().Projection; p.TodoWritten || len(p.Todos) > 0 {
		b, _ := json.Marshal(p.Todos)
		t.Fatalf("late worker changed todos: %s", b)
	}
	if got := c.runGuarded(func(context.Context) error { return nil }); got != turnDroppedWriteAuthority {
		t.Fatalf("sealed runtime admitted another turn: %v", got)
	}
}

func TestWatchdogPlanPreservesAcceptedCompactionSummary(t *testing.T) {
	c, _, _ := exclusiveTestController(t, event.Discard)
	input := provider.Message{ID: "watchdog-input", Role: provider.RoleUser, Content: "real input"}
	c.noteTerminationBoundary(input, true)
	summary := provider.Message{ID: "watchdog-summary", Role: provider.RoleUser, Content: "<compaction-summary>\nearlier work\n</compaction-summary>"}
	if err := c.replaceSessionModelContext(t.Context(), []provider.Message{summary, input}, "test-compaction"); err != nil {
		t.Fatal(err)
	}
	c.turnEvents.commitMu.Lock()
	p, err := c.watchdogTerminationPlanLocked(c.sessionEventStore(), "watchdog-turn")
	c.turnEvents.commitMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 2 || p.Messages[0].ID != summary.ID || p.Messages[1].ID != input.ID {
		t.Fatalf("watchdog lost accepted context: %+v", p.Messages)
	}
}

func TestTerminationCommitStorageFailureRequiresRecovery(t *testing.T) {
	states := make(chan event.RuntimeStateSnapshot, 32)
	c, _, _ := exclusiveTestController(t, &runtimeStateTestSink{Sink: event.Discard, states: states})
	started := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		c.stripCancelledVisibleTurnMessagesAfterWithFallback(c.executor.Session().Len(), provider.Message{ID: "input", Role: provider.RoleUser, Content: "keep input"})
		return ctx.Err()
	})
	awaitPromptLedgerTest(t, started, "turn start")
	blockPromptTestLedger(t, c, "")
	c.CancelSession()
	runtimeStateAwait(t, states, func(s event.RuntimeStateSnapshot) bool { return s.Phase == "recovery_required" })
	if got := c.runGuarded(func(context.Context) error { return nil }); got != turnDroppedWriteAuthority {
		t.Fatalf("failed terminal allowed send: %v", got)
	}
}

type terminationSyncFailurePersistence struct {
	*session.FilesystemPersistence
	armed  atomic.Bool
	store  *session.Session
	failed chan session.Snapshot
}

func (p *terminationSyncFailurePersistence) Create(options session.CreateOptions) (*session.Session, error) {
	store, err := session.CreateWithOptions(filepath.Join(p.Root, options.SessionID), options.SessionID, session.OpenOptions{Sync: func(file *os.File) error {
		if p.armed.Load() {
			snapshot := p.store.StateSnapshot()
			// Fail only after the terminal was accepted into the execution
			// projection; earlier status/autosave flushes must remain healthy.
			if snapshot.Projection.TurnID == "" {
				select {
				case p.failed <- snapshot:
				default:
				}
				return errors.New("injected accepted-terminal fsync failure")
			}
		}
		return file.Sync()
	}})
	if err == nil {
		p.store = store
	}
	return store, err
}

func TestTerminationCommitAcceptedFlushFailureRequiresRecovery(t *testing.T) {
	persistence := &terminationSyncFailurePersistence{FilesystemPersistence: session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")), failed: make(chan session.Snapshot, 4)}
	service, err := session.NewService("desktop", persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { persistence.armed.Store(false); _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "flush-failure"})
	if err != nil {
		t.Fatal(err)
	}
	states := make(chan event.RuntimeStateSnapshot, 32)
	sink := &runtimeStateTestSink{Sink: event.Discard, states: states}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, sink)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: sink, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	t.Cleanup(func() { persistence.armed.Store(false) })
	started := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		c.stripCancelledVisibleTurnMessagesAfterWithFallback(c.executor.Session().Len(), provider.Message{ID: "accepted-input", Role: provider.RoleUser, Content: "accepted input"})
		persistence.armed.Store(true)
		return ctx.Err()
	})
	awaitPromptLedgerTest(t, started, "turn start")
	_, terminatingTurnID, _ := c.currentTurnToken()
	c.CancelSession()
	accepted := awaitPromptLedgerTest(t, persistence.failed, "terminal accepted before fsync failure")
	if accepted.EventSequence <= accepted.DurableSequence {
		t.Fatalf("injection did not cover accepted-only tail: %+v", accepted)
	}
	runtimeStateAwait(t, states, func(s event.RuntimeStateSnapshot) bool { return s.Phase == "recovery_required" })
	if got := c.runGuarded(func(context.Context) error { return nil }); got != turnDroppedWriteAuthority {
		t.Fatalf("failed durability admitted another turn: %v", got)
	}
	// Retry only the persistence barrier, never synthesize a second terminal.
	persistence.armed.Store(false)
	assertSingleTerminationCommit(t, terminationCommitHistory(t, c), terminatingTurnID, true)
}

func TestTerminationCommitSyntheticCompactedOutOfModelStillRetracts(t *testing.T) {
	done := make(chan event.Event, 8)
	c, _, _ := exclusiveTestController(t, event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
	}))
	prefix := []provider.Message{
		{ID: "prefix-system", Role: provider.RoleSystem, Content: "system"},
		{ID: "old-user", Role: provider.RoleUser, Content: "old real question"},
		{ID: "old-answer", Role: provider.RoleAssistant, Content: "old real answer"},
	}
	if err := c.RecordSessionMessages(t.Context(), "old-history", prefix); err != nil {
		t.Fatal(err)
	}
	c.restoreExecutorFromSessionEvents()
	started := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		start := c.executor.Session().Len()
		input := provider.Message{ID: "synthetic-input", Role: provider.RoleUser, Content: "internal generated task"}
		c.noteTerminationBoundary(input, false)
		current := []provider.Message{input, {ID: "synthetic-answer", Role: provider.RoleAssistant, Content: "unfinished generated answer"}}
		for _, m := range current {
			c.executor.Session().Add(m)
		}
		if err := c.RecordSessionMessages(ctx, "synthetic-before-compaction", current); err != nil {
			return err
		}
		// Compaction removes both old history and this turn's records from the
		// model workset; neither absence alone authorizes deleting old history.
		compacted := []provider.Message{prefix[0], {ID: "summary", Role: provider.RoleUser, Content: "<compaction-summary>\nearlier work\n</compaction-summary>"}}
		if err := c.replaceSessionModelContext(ctx, compacted, "test-auto-compaction"); err != nil {
			return err
		}
		c.executor.Session().Replace(compacted)
		close(started)
		<-ctx.Done()
		c.stripInterruptedSyntheticTurnMessagesAfter(start)
		return ctx.Err()
	})
	awaitPromptLedgerTest(t, started, "compacted synthetic turn")
	c.CancelSession()
	terminal := waitTurnDoneEvent(t, done)
	waitIdleAdmission(t, c)
	commits := terminationCommitHistory(t, c)
	// The earlier context replacement is compaction, not terminal cleanup.
	var ends int
	for _, commit := range commits {
		for _, e := range commit.Events {
			if e.Kind == "history/replace" {
				t.Fatal("synthetic cancellation rewrote history")
			}
			if e.Kind == "turn/end" && commit.TurnID == terminal.TurnID {
				ends++
			}
		}
	}
	if ends != 1 {
		t.Fatalf("terminal count=%d", ends)
	}
	projection := c.sessionEventStore().Snapshot().Projection
	retained := map[string]bool{}
	for _, m := range projection.Messages {
		retained[m.ID] = true
		if m.ID == "synthetic-input" || m.ID == "synthetic-answer" {
			t.Fatalf("compacted current-turn record survived: %s", m.ID)
		}
	}
	if !retained["old-user"] || !retained["old-answer"] {
		t.Fatalf("compaction prefix was incorrectly retracted: %+v", retained)
	}
	summaryKept := false
	for _, m := range projection.ModelMessages {
		summaryKept = summaryKept || m.ID == "summary"
	}
	if !summaryKept {
		t.Fatal("compaction summary was removed from model context")
	}
}

func TestTerminationCommitRetainsUnresolvedSideEffectOnce(t *testing.T) {
	done := make(chan event.Event, 8)
	c, _, _ := exclusiveTestController(t, event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
	}))
	record := provider.ToolCallRecord{Identity: provider.ActionIdentity{AttemptID: "attempt-write", CallID: "write-1"}, Arguments: json.RawMessage(`{"path":"output.txt"}`), State: provider.ToolRunUnknown, ReadOnly: false, EffectSummary: "effect_unknown"}
	started := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		start := c.executor.Session().Len()
		input := provider.Message{ID: "synthetic-input", Role: provider.RoleUser, Content: "internal task"}
		c.noteTerminationBoundary(input, false)
		messages := []provider.Message{input, {ID: "side-effect-call", Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "write-1", Name: "write_file", Arguments: `{"path":"output.txt"}`, Recovery: &record}}}}
		for _, message := range messages {
			c.executor.Session().Add(message)
		}
		if err := c.RecordSessionMessages(ctx, "side-effect-record", messages); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		c.stripTurnMessagesAfter(start)
		return ctx.Err()
	})
	awaitPromptLedgerTest(t, started, "side-effect record")
	c.CancelSession()
	c.CancelSession()
	terminal := waitTurnDoneEvent(t, done)
	waitIdleAdmission(t, c)
	assertSingleTerminationCommit(t, terminationCommitHistory(t, c), terminal.TurnID, true)
	findRecord := func(messages []provider.Message) string {
		t.Helper()
		count := 0
		id := ""
		for _, message := range messages {
			if message.ID == "synthetic-input" || message.ID == "side-effect-call" {
				t.Fatalf("synthetic source survived: %s", message.ID)
			}
			for _, call := range message.ToolCalls {
				if call.Recovery != nil && call.Recovery.Identity.AttemptID == "attempt-write" {
					count++
					id = message.ID
					if !message.LocalOnly || id == "" || call.Recovery.State != provider.ToolRunUnknown || string(call.Recovery.Arguments) != string(record.Arguments) {
						t.Fatalf("retained record changed: %+v", message)
					}
				}
			}
		}
		if count != 1 {
			t.Fatalf("retained side effect count=%d want=1; messages=%+v", count, messages)
		}
		return id
	}
	durableID := findRecord(c.sessionEventStore().Snapshot().Projection.Messages)
	if executorID := findRecord(c.executor.Session().Snapshot()); executorID != durableID {
		t.Fatalf("executor invented ghost ID %q, durable=%q", executorID, durableID)
	}
	if pending := c.executor.PendingToolRecovery(); len(pending) != 1 || pending[0].Identity.AttemptID != "attempt-write" {
		t.Fatalf("pending side-effect fact=%+v", pending)
	}
	// Reinstalling the accepted context must not invoke Replace's retention
	// fallback a second time and generate an uncommitted LocalOnly ghost.
	c.executor.Session().Replace(c.executor.Session().Snapshot())
	if id := findRecord(c.executor.Session().Snapshot()); id != durableID {
		t.Fatalf("second Replace invented ghost ID %q", id)
	}
	if err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if id := findRecord(c.sessionEventStore().Snapshot().Projection.Messages); id != durableID {
		t.Fatalf("autosave changed retained identity %q", id)
	}
}
