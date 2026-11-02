package provider

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func compatibilityTestClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	c := WithProtocolCompatibility(server.Client(), RequestIdentity{Provider: t.Name(), Protocol: "openai"})
	tr := c.Transport.(*protocolTransport)
	tr.hints = freshProtocolHints()
	t.Cleanup(c.CloseIdleConnections)
	return c
}

// primeCompatibility fails two separate requests with INTERNAL_ERROR, the
// evidence a route needs before a later request may try HTTP/1.1.
func primeCompatibility(t *testing.T, client *http.Client, endpoint string) {
	t.Helper()
	for range 2 {
		resp, err := compatibilityPost(t.Context(), client, endpoint)
		if resp != nil {
			_, err = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if HTTP2TransportCode(err) != "INTERNAL_ERROR" {
			t.Fatalf("missing typed HTTP/2 failure: %v", err)
		}
	}
}

func compatibilityPost(ctx context.Context, client *http.Client, endpoint string) (*http.Response, error) {
	return SendWithRetry(ctx, client, SendOptions{Provider: "test", Protocol: "openai"}, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"messages":[{"role":"user","content":"unchanged"}]}`))
	})
}

func TestProtocolCompatibilityUsesOnlySubsequentRequests(t *testing.T) {
	for _, bodyFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers-no-observer", true: "partial-stream-observed"}[bodyFailure], func(t *testing.T) {
			var requests, proxies atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				b, err := io.ReadAll(r.Body)
				if err != nil || string(b) != `{"messages":[{"role":"user","content":"unchanged"}]}` {
					t.Error("request body changed")
				}
				if r.ProtoMajor == 2 {
					if bodyFailure {
						_, _ = io.WriteString(w, "data: partial\n\n")
						w.(http.Flusher).Flush()
					}
					panic(http.ErrAbortHandler) // real HTTP/2 RST_STREAM INTERNAL_ERROR
				}
				_, _ = io.WriteString(w, "data: complete\n\n")
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			client := compatibilityTestClient(t, server)
			client.Transport.(*protocolTransport).proxy = func(*http.Request) (*url.URL, error) {
				proxies.Add(1)
				return nil, nil
			}
			var mu sync.Mutex
			var last RequestObservation
			baseCtx := t.Context()
			if bodyFailure {
				baseCtx = WithRequestObserver(baseCtx, func(v RequestObservation) { mu.Lock(); last = v; mu.Unlock() })
			}
			for attempt := 1; attempt <= 4; attempt++ {
				ctx := WithRequestAttemptCounter(baseCtx)
				resp, err := compatibilityPost(ctx, client, server.URL+"/chat")
				if resp != nil {
					_, err = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
				if attempt <= 2 {
					// One INTERNAL_ERROR is a server's generic stream reset as often
					// as an incompatibility; only a later request failing confirms it.
					if HTTP2TransportCode(err) != "INTERNAL_ERROR" {
						t.Fatalf("attempt %d did not stay on HTTP/2: %v", attempt, err)
					}
				} else if err != nil || resp.ProtoMajor != 1 {
					t.Fatalf("subsequent request did not use HTTP/1.1: resp=%v err=%v", resp, err)
				}
				if requests.Load() != int32(attempt) || proxies.Load() != int32(attempt) || RequestAttemptCount(ctx) != 1 {
					t.Fatalf("replayed request or resolved proxy twice: requests=%d proxies=%d count=%d", requests.Load(), proxies.Load(), RequestAttemptCount(ctx))
				}
			}
			mu.Lock()
			if bodyFailure && (last.ProtocolFallbackReason != "INTERNAL_ERROR" || last.HTTPProtocol != "HTTP/1.1" || last.TransportCode != "") {
				t.Errorf("lost compatibility evidence: %+v", last)
			}
			mu.Unlock()
			// Another endpoint remains HTTP/2, even with the same client and provider.
			primeCompatibility(t, client, server.URL+"/other")
		})
	}
}

func TestProtocolCompatibilityFailureKeepsExistingRecovery(t *testing.T) {
	for _, failure := range []string{"headers-eof", "body-eof", "send-timeout"} {
		t.Run(failure, func(t *testing.T) {
			var requests atomic.Int32
			var primed atomic.Bool
			release := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.ProtoMajor == 2 {
					panic(http.ErrAbortHandler)
				}
				if !primed.Load() {
					t.Error("HTTP/1.1 selected before the route was confirmed")
					return
				}
				switch failure {
				case "send-timeout":
					<-release
					return
				case "body-eof":
					_, _ = io.WriteString(w, "data: partial\n\n")
					w.(http.Flusher).Flush()
				}
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			defer close(release)
			server.Client().Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err == nil {
					_ = conn.(*net.TCPConn).SetWriteBuffer(1024)
				}
				return conn, err
			}
			client := compatibilityTestClient(t, server)
			primeCompatibility(t, client, server.URL)
			primed.Store(true)
			var mu sync.Mutex
			observed := map[uint64]RequestObservation{}
			ctx := WithRequestAttemptCounter(WithRequestObserver(t.Context(), func(v RequestObservation) {
				mu.Lock()
				observed[v.ID] = v
				mu.Unlock()
			}))
			resp, err := sendWithResends(ctx, client, SendOptions{}, func(ctx context.Context) (*http.Request, error) {
				body := "small"
				if failure == "send-timeout" {
					body = strings.Repeat("x", 3_248_572)
				}
				return http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(body))
			}, 500*time.Millisecond, 1)
			if resp != nil {
				_, err = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
			var probe RequestObservation
			mu.Lock()
			for _, v := range observed {
				if v.ProtocolFallbackReason != "" {
					probe = v
				}
			}
			mu.Unlock()
			if probe.ProtocolFallbackReason != "INTERNAL_ERROR" || probe.TransportCode != "" {
				t.Fatalf("lost compatibility evidence: %+v", probe)
			}
			if failure == "send-timeout" {
				// The upload never finished, so the existing resend allowance still
				// owns it; the failed probe has already returned the route to HTTP/2.
				if RequestAttemptCount(ctx) != 2 || requests.Load() != 4 {
					t.Fatalf("send timeout lost its resend: server=%d count=%d err=%v", requests.Load(), RequestAttemptCount(ctx), err)
				}
				if probe.ErrorCode != "request_send_timeout" || probe.TimeoutSource != "send_guard" {
					t.Fatalf("lost guarded send failure evidence: %+v", probe)
				}
			} else {
				if RequestAttemptCount(ctx) != 1 || requests.Load() != 3 {
					t.Fatalf("fresh-connection failure was resent: server=%d count=%d", requests.Load(), RequestAttemptCount(ctx))
				}
				// A dropped HTTP/1.1 connection is the same recoverable reset the
				// stream owners already handle, not a protocol failure.
				if recovery := ClassifyRecovery(err); !IsConnReset(err) || recovery.Phase == "protocol" || !recovery.Retryable {
					t.Fatalf("connection failure reclassified: phase=%q retryable=%t err=%v", recovery.Phase, recovery.Retryable, err)
				}
			}
			before := requests.Load()
			// The failed H1 probe disables compatibility until the original expiry.
			_, err = compatibilityPost(t.Context(), client, server.URL)
			if HTTP2TransportCode(err) != "INTERNAL_ERROR" || requests.Load() != before+1 {
				t.Fatalf("failed compatibility looped: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
}
