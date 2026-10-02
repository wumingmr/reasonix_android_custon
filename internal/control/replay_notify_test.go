package control

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/notify"
)

type countingSender struct {
	mu   sync.Mutex
	sent []notify.Message
}

func (s *countingSender) Send(m notify.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

func (s *countingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func notifyingSink(sender notify.Sender, reqs chan<- event.Event) event.Sink {
	return notify.NewSink(event.FuncSink(func(e event.Event) {
		if e.Kind == event.ApprovalRequest || e.Kind == event.AskRequest {
			reqs <- e
		}
	}), sender, config.NotificationsConfig{Enabled: true, ApprovalRequest: true, AskRequest: true})
}

func approveAsync(c *Controller) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = gateApprover{c}.Approve(context.Background(), "bash", "go test ./...", json.RawMessage(`{"command":"go test ./..."}`))
	}()
	return done
}

// The desktop replays a blocked prompt whenever it reconciles a tab, through
// the same notification-wrapped sink as the original request. A notification
// announces a request; a replay only rebuilds its card (#9156).
func TestReplayedPromptDoesNotNotifyAgain(t *testing.T) {
	sender := &countingSender{}
	reqs := make(chan event.Event, 8)
	c := newOwnedTestController(t, Options{Sink: notifyingSink(sender, reqs)})

	done := approveAsync(c)
	first := <-reqs
	for range 3 {
		c.ReplayPendingPrompts()
		if replayed := <-reqs; !replayed.Replayed {
			t.Fatal("a replayed approval is not marked as a replay")
		}
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("notifications for one approval replayed 3 times = %d, want 1", got)
	}
	c.Approve(first.Approval.ID, true, false, false)
	<-done

	askDone := make(chan struct{})
	go func() {
		defer close(askDone)
		_, _ = c.Ask(context.Background(), []event.AskQuestion{{ID: "q1", Prompt: "Which?", Options: []event.AskOption{{Label: "A"}}}})
	}()
	ask := <-reqs
	c.ReplayPendingPrompts()
	<-reqs
	if got := sender.count(); got != 2 {
		t.Fatalf("notifications after a new ask and one replay = %d, want 2", got)
	}
	c.AnswerQuestion(ask.Ask.ID, []event.AskAnswer{{QuestionID: "q1", Selected: []string{"A"}}})
	<-askDone
}

// The TUI keeps one notification sink across /reload and model switches while
// each rebuilt controller numbers its prompts from "1" again. Two requests are
// two requests even when they share an id.
func TestRebuiltControllersSharingASinkEachNotify(t *testing.T) {
	sender := &countingSender{}
	reqs := make(chan event.Event, 8)
	sink := notifyingSink(sender, reqs)
	for i := range 2 {
		c := newOwnedTestController(t, Options{Sink: sink})
		done := approveAsync(c)
		req := <-reqs
		if req.Approval.ID != "1" {
			t.Fatalf("controller %d first approval id = %q, want 1", i, req.Approval.ID)
		}
		c.Approve(req.Approval.ID, true, false, false)
		<-done
	}
	if got := sender.count(); got != 2 {
		t.Fatalf("notifications for two controllers' approval 1 = %d, want 2", got)
	}
}
