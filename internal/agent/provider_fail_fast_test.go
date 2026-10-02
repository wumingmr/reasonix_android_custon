package agent

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestProviderFailureReturnsWithoutWaitingOrRetrying(t *testing.T) {
	for _, role := range []string{"main", "subagent", "planner"} {
		for _, cause := range []error{
			&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			&provider.APIError{Status: 503},
			&provider.APIError{Status: 429, RetryAfter: time.Hour},
			&provider.APIError{Status: 409},
		} {
			t.Run(role+"/"+cause.Error(), func(t *testing.T) {
				p := testutil.NewMock("unavailable", testutil.Turn{StreamError: cause}, testutil.Turn{Text: "must not retry"})
				sink := &recordSink{}
				ctx, cancel := context.WithCancel(withNoClosedLoop(t.Context()))
				defer cancel()
				a := New(p, echoRegistry(), NewSession(""), Options{}, event.FuncSink(func(e event.Event) {
					sink.Emit(e)
					if e.Kind == event.Retrying {
						cancel() // A regression must fail without actually waiting for Retry-After.
					}
				}))
				if role == "subagent" {
					ctx = WithSubagentDepth(ctx, 1)
				}
				if role == "planner" {
					ctx = context.WithValue(ctx, turnContextRoleKey{}, turnContextPlanner)
				}
				if err := a.Run(ctx, "go"); !errors.Is(err, cause) {
					t.Fatalf("lost original failure: %v", err)
				}
				if p.CallCount() != 1 || len(sink.kinds(event.Retrying)) != 0 {
					t.Fatalf("calls=%d retries=%+v", p.CallCount(), sink.kinds(event.Retrying))
				}
			})
		}
	}
}
