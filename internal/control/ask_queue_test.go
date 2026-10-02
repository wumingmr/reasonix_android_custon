package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
)

func askProbeQuestions() []event.AskQuestion {
	return []event.AskQuestion{{
		ID: "q1", Header: "Fix", Prompt: "Which fix?",
		Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
	}}
}

type askProbeSink struct {
	mu   sync.Mutex
	asks []event.Ask
}

func (s *askProbeSink) Emit(e event.Event) {
	if e.Kind != event.AskRequest {
		return
	}
	s.mu.Lock()
	s.asks = append(s.asks, e.Ask)
	s.mu.Unlock()
}

func (s *askProbeSink) snapshot() []event.Ask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.Ask(nil), s.asks...)
}

// Independent interactions must all become visible without waiting for an
// earlier answer. The frontend decides which card to put on top; the registry
// retains every pending request and each waiter has its own reply channel.
func TestConcurrentAsksArePublishedAndResolvedIndependently(t *testing.T) {
	sink := &askProbeSink{}
	c := newOwnedTestController(t, Options{Sink: sink, SessionDir: t.TempDir()})

	ctx := t.Context()
	type result struct {
		answers []event.AskAnswer
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			answers, err := c.Ask(ctx, askProbeQuestions())
			results <- result{answers: answers, err: err}
		}()
	}

	deadline := time.After(2 * time.Second)
	var asks []event.Ask
	for len(asks) != 2 {
		select {
		case <-deadline:
			t.Fatalf("published asks = %d, want 2", len(asks))
		case <-time.After(5 * time.Millisecond):
			asks = sink.snapshot()
		}
	}
	if asks[0].ID == asks[1].ID {
		t.Fatalf("concurrent asks share id %q", asks[0].ID)
	}
	if _, pending := c.approval.snapshotPrompts(); len(pending) != 2 {
		t.Fatalf("pending asks = %d, want 2", len(pending))
	}

	for _, ask := range asks {
		c.AnswerQuestion(ask.ID, []event.AskAnswer{{QuestionID: "q1", Selected: []string{ask.ID}}})
	}
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil || len(got.answers) != 1 {
				t.Fatalf("Ask result = %#v, %v", got.answers, got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Ask stayed blocked after its answer")
		}
	}
}

// Ask has no timeout of its own: approvalTimeout defaults to zero, so a
// question nobody answers blocks its turn until the user cancels.
func TestAskWithoutTimeoutBlocksUntilCancelled(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard, SessionDir: t.TempDir()})
	if c.approval.approvalTimeout != 0 {
		t.Skipf("approvalTimeout is %v; this test pins the unbounded default", c.approval.approvalTimeout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := c.Ask(ctx, askProbeQuestions())
		errc <- err
	}()

	select {
	case err := <-errc:
		t.Fatalf("Ask returned %v without an answer; it is expected to block", err)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("cancelled Ask returned a nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not unblock after cancellation")
	}
}
