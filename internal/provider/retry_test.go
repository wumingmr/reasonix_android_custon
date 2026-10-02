package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func statusResp(status int, hdr map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("body")), Header: h}
}

func newDummyReq(ctx context.Context) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, "http://x/y", nil)
}

func TestRetryableStatus(t *testing.T) {
	for _, s := range []int{408, 429, 500, 502, 503, 504, 529, 599} {
		if !RetryableStatus(s) {
			t.Errorf("status %d should be retryable", s)
		}
	}
	for _, s := range []int{200, 400, 401, 402, 403, 404, 422} {
		if RetryableStatus(s) {
			t.Errorf("status %d should not be retryable", s)
		}
	}
}

func TestSendWithRetryCarriesDisplayIdentityAndSanitizedRequestPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	_, err := SendWithRetry(context.Background(), server.Client(), SendOptions{
		Provider: "deepseek-anthropic", ProviderDisplayName: "Deepseek2", Protocol: "openai",
	}, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/anthropic/v1/chat/completions?token=secret", nil)
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if apiErr.Provider != "deepseek-anthropic" || apiErr.ProviderDisplayName != "Deepseek2" || apiErr.Protocol != "openai" || apiErr.RequestPath != "/anthropic/v1/chat/completions" {
		t.Fatalf("API error identity = %+v", apiErr)
	}
	if strings.Contains(apiErr.RequestPath, "secret") {
		t.Fatalf("query leaked into request path: %q", apiErr.RequestPath)
	}
}

func TestIsConnReset(t *testing.T) {
	if IsConnReset(nil) {
		t.Error("nil is not a conn reset")
	}
	if IsConnReset(context.Canceled) || IsConnReset(context.DeadlineExceeded) {
		t.Error("ctx cancel/deadline must not look like a recoverable reset")
	}
	if IsConnReset(errors.New("decode stream: invalid character")) {
		t.Error("a plain protocol error must not be treated as a conn reset")
	}
	for _, err := range []error{
		io.ErrUnexpectedEOF,
		&net.OpError{Op: "read", Err: syscall.ECONNRESET},
		fmt.Errorf("read stream: %w", &net.OpError{Op: "read", Err: errors.New("wsarecv: forcibly closed")}),
	} {
		if !IsConnReset(err) {
			t.Errorf("want conn reset for %v", err)
		}
	}
}

func TestParseRetryAfterAcceptsHTTPDate(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))
	if d := parseRetryAfter(resp); d < 25*time.Second || d > 31*time.Second {
		t.Errorf("http-date Retry-After = %v, want ~30s", d)
	}

	resp.Header.Set("Retry-After", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat))
	if d := parseRetryAfter(resp); d != 0 {
		t.Errorf("elapsed http-date Retry-After = %v, want 0", d)
	}
}

func TestSendWithRetryFailsFastOnClientErrors(t *testing.T) {
	for _, status := range []int{400, 402, 422} {
		calls := 0
		cl := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return statusResp(status, nil), nil
		})}
		_, err := SendWithRetry(context.Background(), cl, SendOptions{Provider: "p", KeyEnv: "KEY"}, newDummyReq)
		if calls != 1 {
			t.Errorf("status %d retried (%d calls), should fail fast", status, calls)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status {
			t.Errorf("status %d: want *APIError with Status=%d, got %v", status, status, err)
		}
	}
}

func TestSendWithRetryPreservesProviderTraceID(t *testing.T) {
	cl := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return statusResp(422, map[string]string{"trace_id": "minimax-trace-123"}), nil
	})}
	_, err := SendWithRetry(context.Background(), cl, SendOptions{Provider: "minimax-cn-api"}, newDummyReq)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.TraceID != "minimax-trace-123" {
		t.Fatalf("TraceID = %q, want minimax-trace-123", apiErr.TraceID)
	}
}

func TestSendWithRetryAuthError(t *testing.T) {
	calls := 0
	cl := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return statusResp(401, nil), nil
	})}
	_, err := SendWithRetry(context.Background(), cl, SendOptions{Provider: "deepseek", KeyEnv: "DEEPSEEK_API_KEY", KeyPresent: true}, newDummyReq)
	if calls != 1 {
		t.Errorf("401 retried (%d calls), should fail fast for a never-authed key", calls)
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) || authErr.KeyEnv != "DEEPSEEK_API_KEY" {
		t.Errorf("want *AuthError naming the key env, got %v", err)
	}
	if authErr != nil && authErr.Body != "body" {
		t.Errorf("AuthError should carry the response body, got %q", authErr.Body)
	}
}

func TestSendWithRetryKnownKeyFailsWithoutRetry(t *testing.T) {
	for _, status := range []int{401, 403} {
		calls := 0
		cl := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) { calls++; return statusResp(status, nil), nil })}
		_, err := SendWithRetry(t.Context(), cl, SendOptions{Provider: "mimo", KeyPresent: true, RetryAuth: true}, newDummyReq)
		var auth *AuthError
		if calls != 1 || !errors.As(err, &auth) || auth.Status != status || !auth.HasKey {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	}
}

// stallingBody sends headers' worth of promise and then never delivers: Read
// blocks until Close, mimicking a half-open 502/524 gateway that stalls after
// the status line. Close is what the errorBodyReadTimeout timer fires.
type stallingBody struct {
	closeOnce sync.Once
	closed    chan struct{}
}

func newStallingBody() *stallingBody { return &stallingBody{closed: make(chan struct{})} }

func (b *stallingBody) Read(p []byte) (int, error) {
	<-b.closed
	return 0, errors.New("body closed")
}

func (b *stallingBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

// TestSendWithRetryUnblocksStalledErrorBody locks in the #6607 freeze fix: a
// failed response whose body never arrives must not wedge error reporting —
// the deadline closes the body and the original HTTP failure is returned.
// Without the timer in readErrorBody this test hangs on
// the first 502 body and fails via the watchdog below.
func TestSendWithRetryUnblocksStalledErrorBody(t *testing.T) {
	prev := errorBodyReadTimeout
	errorBodyReadTimeout = 50 * time.Millisecond
	defer func() { errorBodyReadTimeout = prev }()

	calls := 0
	cl := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusBadGateway, Body: newStallingBody(), Header: http.Header{}}, nil
		}
		return statusResp(200, nil), nil
	})}

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := SendWithRetry(context.Background(), cl, SendOptions{Provider: "p", KeyEnv: "KEY"}, newDummyReq)
		done <- result{resp, err}
	}()

	select {
	case r := <-done:
		var api *APIError
		if r.resp != nil || !errors.As(r.err, &api) || api.Status != http.StatusBadGateway || calls != 1 {
			t.Fatalf("calls=%d err=%v, want original 502 without retry", calls, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendWithRetry wedged on a stalled error body — read deadline did not fire")
	}
}

func TestSendWithRetryReturnsUpstreamFailureWithoutNotification(t *testing.T) {
	for _, status := range []int{408, 409, 429, 500, 503, 529} {
		calls, notifications := 0, 0
		cl := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return statusResp(status, map[string]string{"Retry-After": "120"}), nil
		})}
		ctx, cancel := context.WithCancel(t.Context())
		ctx = WithRequestAttemptCounter(WithRetryNotify(ctx, func(RetryInfo) { notifications++; cancel() }))
		_, err := SendWithRetry(ctx, cl, SendOptions{}, newDummyReq)
		cancel()
		var api *APIError
		if !errors.As(err, &api) || api.Status != status || calls != 1 || notifications != 0 || RequestAttemptCount(ctx) != 1 {
			t.Fatalf("status=%d calls=%d notifications=%d err=%v", status, calls, notifications, err)
		}
		if api.RetryAfter != 2*time.Minute {
			t.Fatalf("lost upstream diagnostic: %+v", api)
		}
	}
}

func TestRequestAttemptCountTracksExplicitRequests(t *testing.T) {
	calls := 0
	cl := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return statusResp(http.StatusServiceUnavailable, nil), nil
		}
		return statusResp(http.StatusBadRequest, nil), nil
	})}
	ctx := WithRequestAttemptCounter(context.Background())
	providerCtx := WithRequestAttemptCounter(ctx)

	for range 3 {
		if _, err := SendWithRetry(providerCtx, cl, SendOptions{Provider: "p"}, newDummyReq); err == nil {
			t.Fatal("expected terminal provider error")
		}
	}
	if calls != 3 {
		t.Fatalf("calls=%d, want one per explicit request", calls)
	}
	if got := RequestAttemptCount(ctx); got != 3 {
		t.Fatalf("request attempt count = %d, want 3", got)
	}
	usage := UsageWithRequestAttemptCount(ctx, nil)
	if usage == nil || usage.TotalTokens != 0 || usage.RequestCount != 3 {
		t.Fatalf("failed request usage = %+v, want tokens=0 requests=3", usage)
	}
}

func TestIndependentRequestAttemptCounter(t *testing.T) {
	parent := WithRequestAttemptCounter(context.Background())
	recordRequestAttempt(parent)
	child := WithIndependentRequestAttemptCounter(parent)
	recordRequestAttempt(child)
	recordRequestAttempt(child)
	if RequestAttemptCount(parent) != 1 || RequestAttemptCount(child) != 2 {
		t.Fatalf("auxiliary and main request counts leaked: parent=%d child=%d", RequestAttemptCount(parent), RequestAttemptCount(child))
	}
}
