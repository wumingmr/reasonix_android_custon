package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

const wantPrecedence = "The user's new message takes precedence. If it is unrelated to the interrupted task, do not resume that task."

func TestInterruptedRecoveryBlockStatesUserMessagePrecedence(t *testing.T) {
	apiErr := errors.New("upstream reset")
	mp := testutil.NewMock("m",
		testutil.Turn{Text: "partial", ChunkError: apiErr},
		testutil.Turn{Text: "ok"},
	)
	session := NewSession("system")
	a := New(mp, echoRegistry(), session, Options{}, event.Discard)
	if err := a.Run(withNoClosedLoop(context.Background()), "refactor the parser"); !errors.Is(err, apiErr) {
		t.Fatalf("first Run error = %v, want %v", err, apiErr)
	}
	const next = "list the files in the docs folder"
	if err := a.Run(withNoClosedLoop(context.Background()), next); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	msgs := mp.Requests()[1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleUser {
		t.Fatalf("last message role = %v, want user", last.Role)
	}
	if !strings.HasPrefix(last.Content, "<"+interruptedRecoveryTag+">") || !strings.HasSuffix(last.Content, next) {
		t.Fatalf("recovery block must precede the user text at the tail: %q", last.Content)
	}
	if !strings.Contains(last.Content, wantPrecedence) {
		t.Fatalf("recovery block missing precedence clause: %q", last.Content)
	}
	if strings.Index(last.Content, wantPrecedence) > strings.Index(last.Content, "</"+interruptedRecoveryTag+">") {
		t.Fatalf("precedence clause must sit inside the recovery block: %q", last.Content)
	}
	for _, m := range msgs[:len(msgs)-1] {
		if strings.Contains(m.Content, interruptedRecoveryTag) {
			t.Fatalf("recovery block leaked into the stable prefix: %+v", m)
		}
	}
}
