package control

import (
	"errors"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/sessioninbox"
	"reasonix/internal/tool"
)

func TestTargetGuidanceDoesNotSteerSuccessorTurn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	prov := &inboxSteerProvider{started: make(chan struct{}), release: make(chan struct{})}
	exec := agent.New(prov, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Runner: exec, Executor: exec, SessionDir: dir, SessionPath: path, Sink: event.Discard})
	c.Submit("successor turn")
	prov.awaitStarted(t, c)
	current := c.RuntimeStatus().TurnID
	if current == "" {
		t.Fatal("active turn has no identity")
	}
	stale, err := c.InboxQueue(path, InboxQueueRequest{Kind: "enqueue_steer", TurnID: "previous-turn", Text: "for the previous turn", IdempotencyKey: "stale"})
	if err != nil || stale.Receipt == nil || stale.Receipt.Disposition != sessioninbox.DispositionQueuedFollowup {
		t.Fatalf("stale guidance: %+v %v", stale, err)
	}
	meta, _, err := c.ReadInboxItem(stale.Receipt.ItemID)
	if err != nil || meta.State != sessioninbox.StateQueued || meta.Intent != sessioninbox.IntentFollowup {
		t.Fatalf("stale item: %+v %v", meta, err)
	}
	matching, err := c.InboxQueue(path, InboxQueueRequest{Kind: "enqueue_steer", TurnID: current, Text: "for the current turn", IdempotencyKey: "matching"})
	if err != nil || matching.Receipt == nil || matching.Receipt.Disposition != sessioninbox.DispositionSteerAccepted {
		t.Fatalf("matching guidance: %+v %v", matching, err)
	}
	if c.RuntimeStatus().TurnID != current {
		t.Fatal("guidance replaced the running turn")
	}
}

func TestInboxExpectedSessionCannotSubmitOrConfirmReplacement(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.jsonl"), filepath.Join(dir, "second.jsonl")
	c := newOwnedTestController(t, Options{SessionDir: dir, SessionPath: first, Sink: event.Discard})
	defer c.Close()
	if err := c.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	request := InboxRequest{ExpectedSessionPath: first, Submit: "original", Idempotency: "original"}
	receipt, err := c.TryEnqueueFollowup(request)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, found, err := c.LookupInboxReceiptForSession(first, request.Idempotency)
	if err != nil || !found || confirmed.ItemID != receipt.ItemID {
		t.Fatalf("original confirmation = %+v, %v, %v", confirmed, found, err)
	}
	c.SetSessionPath(second)
	if err := c.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TryEnqueueFollowup(request); !errors.Is(err, ErrInboxSessionChanged) {
		t.Fatalf("stale request = %v", err)
	}
	if _, _, err := c.LookupInboxReceiptForSession(first, request.Idempotency); !errors.Is(err, ErrInboxSessionChanged) {
		t.Fatalf("stale lookup = %v", err)
	}
	if got := c.InboxSnapshot(); got.SessionPath != second || len(got.Items) != 0 {
		t.Fatalf("replacement mutated: %+v", got)
	}
	request.ExpectedSessionPath = ""
	if _, err := c.TryEnqueueFollowup(request); err != nil {
		t.Fatalf("legacy request no longer works: %v", err)
	}
}

func TestCanonicalInboxUsesSessionIdentityAcrossQueueOperations(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service})
	if _, err := c.BindFreshSession(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	const first = "session-id:first"
	if err := c.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	receipt, err := c.TryEnqueueFollowup(InboxRequest{ExpectedSessionPath: first, Submit: "follow up", Idempotency: "request"})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, found, err := c.LookupInboxReceiptForSession(first, "request")
	if err != nil || !found || confirmed.ItemID != receipt.ItemID {
		t.Fatalf("canonical receipt: %+v %v %v", confirmed, found, err)
	}
	result, err := c.InboxQueue(first, InboxQueueRequest{Kind: "snapshot"})
	if err != nil || result.Outcome != "unchanged" || result.Snapshot.SessionPath != first || len(result.Snapshot.Items) != 1 {
		t.Fatalf("canonical queue: %+v %v", result, err)
	}
	result, err = c.InboxQueue(first, InboxQueueRequest{Kind: "read", ItemID: receipt.ItemID})
	if err != nil || result.Edit == nil || result.Edit.Text != "follow up" {
		t.Fatalf("canonical queue read: %+v %v", result, err)
	}
	if _, err := c.trySteerInboxItemForSession(receipt.ItemID, "old-turn", first); errors.Is(err, ErrInboxSessionChanged) {
		t.Fatal("same-session steer rejected its identity")
	}
	if _, err := c.BindFreshSession(t.Context(), "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnqueueInbox(InboxRequest{ExpectedSessionPath: first, Submit: "stale"}); !errors.Is(err, ErrInboxSessionChanged) {
		t.Fatalf("stale canonical enqueue: %v", err)
	}
	if _, _, err := c.LookupInboxReceiptForSession(first, "request"); !errors.Is(err, ErrInboxSessionChanged) {
		t.Fatalf("stale canonical receipt: %v", err)
	}
	result, err = c.InboxQueue(first, InboxQueueRequest{Kind: "snapshot"})
	if err != nil || result.Reason != "session_changed" {
		t.Fatalf("stale canonical queue: %+v %v", result, err)
	}
	if got := c.InboxSnapshot(); got.SessionPath != "session-id:second" || len(got.Items) != 0 {
		t.Fatalf("queue crossed the session boundary: %+v", got)
	}
}

func TestCanonicalInboxAcceptsMessagesDuringTurnAndDispatchesFIFO(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	runner := &gatedInboxDispatchRunner{inputs: make(chan string, 8), firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	done := make(chan struct{}, 8)
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Runner: runner, SessionService: service, Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- struct{}{}
		}
	})})
	if _, err := c.BindFreshSession(t.Context(), "active-queue"); err != nil {
		t.Fatal(err)
	}
	c.Submit("active turn")
	inputs := &inboxDispatchRunner{inputs: runner.inputs}
	if got := waitForInboxDispatch(t, c, inputs); got != "active turn" {
		t.Fatalf("initial input: %q", got)
	}
	for _, text := range []string{"queued one", "queued two"} {
		receipt, err := c.TryEnqueueFollowup(InboxRequest{ExpectedSessionPath: "session-id:active-queue", Submit: text, Idempotency: text})
		if err != nil || receipt.ItemID == "" {
			t.Fatalf("running canonical enqueue: %+v %v", receipt, err)
		}
	}
	close(runner.releaseFirst)
	waitForInboxTurnDone(t, c, done)
	for _, want := range []string{"queued one", "queued two"} {
		if got := waitForInboxDispatch(t, c, inputs); got != want {
			t.Fatalf("canonical FIFO input = %q, want %q", got, want)
		}
		waitForInboxTurnDone(t, c, done)
	}
	if got := c.InboxSnapshot(); got.SessionPath != "session-id:active-queue" || len(got.Items) != 0 || got.Paused {
		t.Fatalf("canonical completion left queued work: %+v", got)
	}
}
