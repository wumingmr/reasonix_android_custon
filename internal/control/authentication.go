package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"reasonix/internal/provider"
)

// AuthenticationStatus describes whether this controller generation may start
// a model request. Ready only means local authentication preconditions pass.
type AuthenticationStatus string

const (
	AuthenticationReady                      AuthenticationStatus = "ready"
	AuthenticationMissingCredential          AuthenticationStatus = "missing_credential"
	AuthenticationRejected                   AuthenticationStatus = "authentication_rejected"
	AuthenticationCredentialStoreUnavailable AuthenticationStatus = "credential_store_unavailable"
)

// AuthenticationState is safe to expose to frontends. It never contains
// credential material.
type AuthenticationState struct {
	Status       AuthenticationStatus `json:"status"`
	ProviderName string               `json:"providerName,omitempty"`
	ModelRef     string               `json:"modelRef,omitempty"`
	KeyEnv       string               `json:"keyEnv,omitempty"`
	HTTPStatus   int                  `json:"httpStatus,omitempty"`
	Code         string               `json:"code,omitempty"`
	Message      string               `json:"message,omitempty"`
}

func (s AuthenticationState) normalized() AuthenticationState {
	if s.Status == "" {
		s.Status = AuthenticationReady
	}
	if s.Code == "" {
		switch s.Status {
		case AuthenticationMissingCredential:
			s.Code = "missing_credential"
		case AuthenticationRejected:
			s.Code = "authentication_rejected"
		case AuthenticationCredentialStoreUnavailable:
			s.Code = "credential_store_unavailable"
		}
	}
	return s
}

func (s AuthenticationState) Ready() bool { return s.normalized().Status == AuthenticationReady }

// AuthenticationError is returned before admission, so blocked input does not
// create a turn, run submission hooks, or persist a user message.
type AuthenticationError struct{ State AuthenticationState }

func (e *AuthenticationError) Error() string {
	s := e.State.normalized()
	if strings.TrimSpace(s.Message) != "" {
		return s.Message
	}
	label := strings.TrimSpace(s.ProviderName)
	if label == "" {
		label = strings.TrimSpace(s.ModelRef)
	}
	if label == "" {
		label = "the selected model connection"
	}
	switch s.Status {
	case AuthenticationMissingCredential:
		return fmt.Sprintf("%s is missing its API key; use /setup here, or exit and run `reasonix setup` in your shell", label)
	case AuthenticationRejected:
		return fmt.Sprintf("%s rejected the configured credential; update it with /setup, choose another model, or retry explicitly", label)
	case AuthenticationCredentialStoreUnavailable:
		return fmt.Sprintf("credentials for %s could not be read; open credential diagnostics before retrying", label)
	default:
		return "model authentication is not ready"
	}
}

type authenticationGate struct {
	mu              sync.RWMutex
	state           AuthenticationState
	primary         string
	rejections      map[string]AuthenticationState
	initialForModel func(string) AuthenticationState
}

func newAuthenticationGate(initial AuthenticationState, primary string) authenticationGate {
	return authenticationGate{state: initial.normalized(), primary: primary, rejections: map[string]AuthenticationState{}}
}

func (g *authenticationGate) snapshot() AuthenticationState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.state.normalized()
}

func (g *authenticationGate) admissionError() error {
	state := g.snapshot()
	if state.Ready() {
		return nil
	}
	return &AuthenticationError{State: state}
}

func (g *authenticationGate) admissionErrorForModel(ref string) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.state.Ready() && (ref == g.primary || (g.state.Status != AuthenticationRejected && sameAuthenticationConnection(ref, g.primary))) {
		return &AuthenticationError{State: g.state}
	}
	if g.initialForModel != nil {
		if state := g.initialForModel(ref); !state.Ready() {
			return &AuthenticationError{State: state}
		}
	}
	for failedRef, state := range g.rejections {
		if ref == failedRef || (state.HTTPStatus == 401 && sameAuthenticationConnection(ref, failedRef)) {
			return &AuthenticationError{State: state}
		}
	}
	return nil
}

func sameAuthenticationConnection(a, b string) bool {
	left, _, leftOK := strings.Cut(a, "/")
	right, _, rightOK := strings.Cut(b, "/")
	return leftOK && rightOK && left == right
}

func (g *authenticationGate) recordFailure(err error, modelRef string) {
	var authErr *provider.AuthError
	if !errors.As(err, &authErr) || authErr == nil {
		return
	}
	if authErr.ModelRef != "" {
		modelRef = authErr.ModelRef
	} else if name, _, _ := strings.Cut(modelRef, "/"); authErr.Provider != "" && authErr.Provider != name {
		// Legacy/custom runners may return an unscoped error from a child.
		// Never attribute that child's failure to the parent's selected model.
		modelRef = authErr.Provider + "/"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := AuthenticationState{Status: AuthenticationRejected, ProviderName: authErr.Provider, ModelRef: modelRef, KeyEnv: authErr.KeyEnv, HTTPStatus: authErr.Status, Code: "authentication_rejected"}
	g.rejections[modelRef] = state
	// Optional operations can target another model. A model-scoped 403 there
	// must not disable the primary chat connection.
	if modelRef == g.primary || (authErr.Status == 401 && sameAuthenticationConnection(modelRef, g.primary)) {
		g.state = state
	}
}

func (g *authenticationGate) BeforeModelRequest(ref string) error {
	return g.admissionErrorForModel(ref)
}
func (g *authenticationGate) ModelRequestFailed(ref string, err error) { g.recordFailure(err, ref) }
func (c *Controller) withAuthentication(ctx context.Context) context.Context {
	return provider.WithRequestGate(ctx, &c.authentication)
}

func (c *Controller) AuthenticationState() AuthenticationState {
	if c == nil {
		return AuthenticationState{Status: AuthenticationReady}
	}
	return c.authentication.snapshot()
}

// RetryAuthentication permits one explicit attempt. Another 401/403 closes
// the gate when that turn completes.
func (c *Controller) RetryAuthentication() bool {
	if c == nil {
		return false
	}
	c.authentication.mu.Lock()
	defer c.authentication.mu.Unlock()
	if c.authentication.state.normalized().Status != AuthenticationRejected {
		return false
	}
	for ref, state := range c.authentication.rejections {
		if ref == c.authentication.state.ModelRef || (state.HTTPStatus == 401 && sameAuthenticationConnection(ref, c.authentication.state.ModelRef)) {
			delete(c.authentication.rejections, ref)
		}
	}
	c.authentication.state.Status = AuthenticationReady
	c.authentication.state.Code = ""
	c.authentication.state.Message = ""
	return true
}
