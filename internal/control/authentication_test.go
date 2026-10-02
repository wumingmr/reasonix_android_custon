package control

import (
	"context"
	"errors"
	"sync"
	"testing"

	"reasonix/internal/provider"
)

type authenticationTestRunner struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (r *authenticationTestRunner) Run(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.err
}

func (r *authenticationTestRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestAuthenticationMissingCredentialRejectsBeforeRunner(t *testing.T) {
	runner := &authenticationTestRunner{}
	c := newOwnedTestController(t, Options{
		Runner:         runner,
		Authentication: AuthenticationState{Status: AuthenticationMissingCredential, ProviderName: "deepseek", ModelRef: "deepseek/chat"},
	})
	err := c.Run(context.Background(), "must not be submitted")
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) || authErr.State.Status != AuthenticationMissingCredential {
		t.Fatalf("Run error = %v, want missing-credential AuthenticationError", err)
	}
	if got := runner.Calls(); got != 0 {
		t.Fatalf("provider runner calls = %d, want 0", got)
	}
	if got := c.Turn(); got != 0 {
		t.Fatalf("admitted turns = %d, want 0", got)
	}
}

func TestAuthenticationRejectionLatchesUntilExplicitRetry(t *testing.T) {
	runner := &authenticationTestRunner{err: &provider.AuthError{Provider: "relay", KeyEnv: "RELAY_API_KEY", Status: 401, HasKey: true}}
	c := newOwnedTestController(t, Options{Runner: runner, ModelRef: "relay/chat"})
	if err := c.Run(context.Background(), "first"); err == nil {
		t.Fatal("first Run unexpectedly succeeded")
	}
	if got := c.AuthenticationState(); got.Status != AuthenticationRejected || got.HTTPStatus != 401 {
		t.Fatalf("authentication state = %+v, want rejected 401", got)
	}
	if err := c.Run(context.Background(), "blocked"); err == nil {
		t.Fatal("blocked Run unexpectedly succeeded")
	}
	if got := runner.Calls(); got != 1 {
		t.Fatalf("provider runner calls after blocked retry = %d, want 1", got)
	}
	if !c.RetryAuthentication() {
		t.Fatal("explicit retry was not accepted")
	}
	if err := c.Run(context.Background(), "explicit retry"); err == nil {
		t.Fatal("explicit retry unexpectedly succeeded")
	}
	if got := runner.Calls(); got != 2 {
		t.Fatalf("provider runner calls after explicit retry = %d, want 2", got)
	}
}
