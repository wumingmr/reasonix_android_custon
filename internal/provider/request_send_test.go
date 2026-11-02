package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sendTransportFunc func(*http.Request) (*http.Response, error)

func (f sendTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sendTestRequest(url, body string) func(context.Context) (*http.Request, error) {
	return func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	}
}

func TestRequestSendGuardStalledUpload(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, observed := range []bool{false, true} {
			t.Run(map[bool]string{false: "http1", true: "http2"}[h2]+map[bool]string{false: "-plain", true: "-observed"}[observed], func(t *testing.T) {
				arrived := make(chan string, 1)
				release := make(chan struct{})
				siblingRelease := make(chan struct{})
				var calls atomic.Int32
				s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path == "/ok" {
						if r.URL.RawQuery == "hold" {
							w.(http.Flusher).Flush()
							<-siblingRelease
						}
						_, _ = io.WriteString(w, "ok")
						return
					}
					arrived <- r.Proto
					<-release // Deliberately never read the upload.
				}))
				s.EnableHTTP2 = h2
				s.StartTLS()
				defer s.Close()
				defer close(siblingRelease)
				defer close(release)
				client := s.Client()
				tr := client.Transport.(*http.Transport)
				tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
					if err == nil {
						_ = conn.(*net.TCPConn).SetWriteBuffer(1024)
					}
					return conn, err
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				ctx = WithRequestAttemptCounter(ctx)
				var mu sync.Mutex
				var last RequestObservation
				if observed {
					ctx = WithRequestObserver(ctx, func(o RequestObservation) { mu.Lock(); last = o; mu.Unlock() })
				}
				done := make(chan error, 1)
				go func() {
					_, stale, err := sendAttemptWithTimeout(ctx, client, sendTestRequest(s.URL, strings.Repeat("x", 3_248_572)), 300*time.Millisecond)
					if stale {
						done <- errors.New("send timeout was classified as stale idle connection")
						return
					}
					done <- err
				}()
				select {
				case proto := <-arrived:
					if (proto == "HTTP/2.0") != h2 {
						t.Fatalf("protocol = %s", proto)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				var siblingBody io.ReadCloser
				if h2 {
					// Reuse the same multiplexed connection while its sibling stalls.
					var reused bool
					sibling := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }})
					r, _, err := sendAttemptWithTimeout(sibling, client, sendTestRequest(s.URL+"/ok?hold", ""), time.Second)
					if err != nil {
						t.Fatal(err)
					}
					siblingBody = r.Body
					if !reused {
						t.Fatal("sibling did not reuse the stalled connection")
					}
				}
				err := <-done
				var timeout *RequestSendTimeoutError
				if !errors.As(err, &timeout) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
					t.Fatalf("send error=%v parent=%v", err, ctx.Err())
				}
				if DiagnoseFailure(err).Kind != "request_send_timeout" || ClassifyRecovery(err).Retryable || IsConnReset(err) {
					t.Fatalf("wrong timeout classification: %v", err)
				}
				want := int32(1)
				if h2 {
					want++
				}
				if calls.Load() != want || RequestAttemptCount(ctx) != int(want) {
					t.Fatalf("calls=%d attempts=%d", calls.Load(), RequestAttemptCount(ctx))
				}
				if observed {
					mu.Lock()
					v := last
					mu.Unlock()
					if v.ErrorCode != "request_send_timeout" || v.TimeoutSource != "send_guard" || v.Phase == "canceled" || v.SendTimeoutMs != 300 || !v.WrittenAt.IsZero() {
						t.Fatalf("observation: %+v", v)
					}
				}
				if h2 {
					// The sibling remains live across cancellation of the stalled stream.
					siblingRelease <- struct{}{}
					body, err := io.ReadAll(siblingBody)
					siblingBody.Close()
					if err != nil || string(body) != "ok" {
						t.Fatalf("sibling interrupted: body=%q err=%v", body, err)
					}
					var reused bool
					request, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }}), http.MethodGet, s.URL+"/ok", nil)
					r, err := client.Do(request)
					if err != nil {
						t.Fatal("timeout broke sibling connection:", err)
					}
					io.Copy(io.Discard, r.Body)
					r.Body.Close()
					if !reused {
						t.Fatal("timeout closed the shared HTTP/2 connection")
					}
				}
			})
		}
	}
}

func TestRequestSendGuardHandsOffAfterWrite(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "stream"}[stream], func(t *testing.T) {
			release := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
				}
				<-release
				if stream {
					_, _ = io.WriteString(w, "data: ok\n\n")
				}
			}))
			defer s.Close()
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			client := s.Client()
			client.Transport.(*http.Transport).ResponseHeaderTimeout = 150 * time.Millisecond
			var v RequestObservation
			var mu sync.Mutex
			ctx := WithRequestObserver(t.Context(), func(o RequestObservation) { mu.Lock(); v = o; mu.Unlock() })
			resp, _, err := sendAttemptWithTimeout(ctx, client, sendTestRequest(s.URL, "small"), 80*time.Millisecond)
			if !stream {
				mu.Lock()
				defer mu.Unlock()
				if err == nil || DiagnoseFailure(err).Kind != "transport_timeout" || v.ErrorPhase != "response_headers" || v.TimeoutSource != "transport" {
					t.Fatalf("err=%v observation=%+v", err, v)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			time.AfterFunc(160*time.Millisecond, unblock)
			body, err := io.ReadAll(resp.Body)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || string(body) != "data: ok\n\n" || v.ErrorCode != "" || v.Phase != "body_eof" {
				t.Fatalf("body=%q err=%v observation=%+v", body, err, v)
			}
		})
	}
}

func TestRequestSendGuardGenerations(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	g := &requestSendGuard{budget: time.Hour, cancel: cancel}
	round := g.begin()
	g.arm(round)
	first := g.generation
	g.written(round, httptrace.WroteRequestInfo{})
	g.arm(round) // Transport internal retry after a successful write.
	g.expire(first)
	if ctx.Err() != nil {
		t.Fatal("old timer canceled a new send")
	}
	round = g.begin()
	g.written(round-1, httptrace.WroteRequestInfo{})
	if g.timer == nil {
		t.Fatal("late write completion disarmed redirect")
	}
	g.expire(g.generation)
	if !errors.As(context.Cause(ctx), new(*RequestSendTimeoutError)) {
		t.Fatal(context.Cause(ctx))
	}
	g.finish()
}

func TestRequestSendGuardRedirectRetryEarlyResponseAndCleanup(t *testing.T) {
	for _, mode := range []string{"redirect", "internal-retry", "early-response", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var requestCtx context.Context
			var calls int
			client := &http.Client{Transport: sendTransportFunc(func(r *http.Request) (*http.Response, error) {
				requestCtx = r.Context()
				calls++
				trace := httptrace.ContextClientTrace(r.Context())
				trace.GetConn("provider.test")
				if mode == "early-response" {
					return &http.Response{StatusCode: http.StatusRequestEntityTooLarge, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("too large")), Request: r}, nil
				}
				if mode == "redirect" && calls == 1 {
					trace.WroteRequest(httptrace.WroteRequestInfo{})
					return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"/next"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				if mode == "internal-retry" {
					trace.WroteRequest(httptrace.WroteRequestInfo{})
					trace.GetConn("provider.test")
				}
				if mode == "cancel" {
					cancel()
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})}
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 10*time.Millisecond)
				defer stop()
			}
			resp, stale, err := sendAttemptWithTimeout(ctx, client, sendTestRequest("https://provider.test/start", "body"), 40*time.Millisecond)
			if stale {
				t.Fatal("unexpected resend")
			}
			switch mode {
			case "early-response":
				if err != nil || resp.StatusCode != http.StatusRequestEntityTooLarge || requestCtx.Err() != nil {
					t.Fatalf("response=%v err=%v", resp, err)
				}
				resp.Body.Close()
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case "deadline":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			default:
				if !errors.As(err, new(*RequestSendTimeoutError)) {
					t.Fatal(err)
				}
			}
			if mode == "early-response" {
				cancel()
				if requestCtx.Err() != nil {
					t.Fatal("released request is still registered with its caller")
				}
			} else if requestCtx.Err() == nil {
				t.Fatal("request context leaked")
			}
			want := 1
			if mode == "redirect" {
				want = 2
			}
			if calls != want {
				t.Fatalf("requests=%d", calls)
			}
		})
	}
}

func TestSendWithRetryResendsOnceAfterSendTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stalls  int
		cancel  bool
		once    bool
		calls   int
		timeout bool
	}{
		{name: "second-attempt-delivers", stalls: 1, calls: 2},
		{name: "send-once-keeps-first-evidence", stalls: 1, once: true, calls: 1, timeout: true},
		{name: "second-timeout-is-terminal", stalls: 2, calls: 2, timeout: true},
		{name: "user-cancel-is-not-resent", stalls: 2, cancel: true, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(WithRequestAttemptCounter(t.Context()))
			defer cancel()
			var calls int
			client := &http.Client{Transport: sendTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				trace := httptrace.ContextClientTrace(r.Context())
				trace.GetConn("provider.test")
				if calls <= tc.stalls {
					if tc.cancel {
						cancel()
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				trace.WroteRequest(httptrace.WroteRequestInfo{})
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
			})}
			resends := 1
			if tc.once {
				resends = 0
			}
			resp, err := sendWithResends(ctx, client, SendOptions{Provider: "deepseek", Protocol: "openai"}, sendTestRequest("https://provider.test/v1", "body"), 30*time.Millisecond, resends)
			if calls != tc.calls || RequestAttemptCount(ctx) != tc.calls {
				t.Fatalf("calls=%d attempts=%d, want %d", calls, RequestAttemptCount(ctx), tc.calls)
			}
			switch {
			case tc.cancel:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v", err)
				}
			case tc.timeout:
				var failure *RequestFailure
				if !errors.As(err, &failure) || !errors.As(err, new(*RequestSendTimeoutError)) || DiagnoseFailure(err).Kind != "request_send_timeout" {
					t.Fatalf("err=%v", err)
				}
			default:
				if err != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("resp=%v err=%v", resp, err)
				}
				resp.Body.Close()
			}
		})
	}
}

// Cancelling a finished HTTP/2 stream's context races its teardown, and the
// next request can find the pooled connection not yet idle.
func TestRequestSendGuardReleasesWithoutCancellingCompletedStream(t *testing.T) {
	for _, mode := range []string{"eof", "close", "parent-cancel-mid-stream"} {
		t.Run(mode, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(t.Context())
			defer cancelParent()
			var requestCtx context.Context
			client := &http.Client{Transport: sendTransportFunc(func(r *http.Request) (*http.Response, error) {
				requestCtx = r.Context()
				httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data")), Request: r}, nil
			})}
			resp, _, err := sendAttemptWithTimeout(parent, client, sendTestRequest("https://provider.test/v1", "body"), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "parent-cancel-mid-stream" {
				cancelParent()
				select {
				case <-requestCtx.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("caller cancellation did not reach the open stream")
				}
				resp.Body.Close()
				return
			}
			if mode == "eof" {
				_, _ = io.ReadAll(resp.Body)
			}
			resp.Body.Close()
			if requestCtx.Err() != nil {
				t.Fatal("completed stream was cancelled")
			}
			cancelParent()
			if requestCtx.Err() != nil {
				t.Fatal("released request is still registered with its caller")
			}
		})
	}
}
