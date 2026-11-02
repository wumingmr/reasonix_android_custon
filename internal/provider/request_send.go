package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

const requestSendTimeout = 300 * time.Second

// RequestSendTimeoutError is a local send-stage failure, not caller cancellation.
// It does not unwrap context.Canceled or implement net.Error, so no generic
// classifier retries it; SendWithRetry alone resends it once.
type RequestSendTimeoutError struct {
	Budget time.Duration
}

func (e *RequestSendTimeoutError) Error() string {
	return fmt.Sprintf("request_send_timeout: request did not finish sending within %s", e.Budget)
}

// requestSendGuard owns only the pre-response send lifetime. Successful writes
// hand timeout ownership to Transport's ResponseHeaderTimeout. Each RoundTrip
// (redirect) and subsequent GetConn (internal retry) can start a new send stage.
// No connection deadline is changed: an HTTP/2 sibling stream remains usable.
type requestSendGuard struct {
	mu         sync.Mutex
	budget     time.Duration
	cancel     context.CancelCauseFunc
	timer      *time.Timer
	generation uint64
	round      uint64
	started    time.Time
	elapsed    time.Duration
	closed     bool
	timedOut   bool
}

func (g *requestSendGuard) begin() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.round++
	return g.round
}

func (g *requestSendGuard) arm(round uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.timedOut || g.timer != nil || round != g.round {
		return
	}
	g.generation++
	generation := g.generation
	g.started = time.Now()
	g.elapsed = 0
	g.timer = time.AfterFunc(g.budget, func() { g.expire(generation) })
}

func (g *requestSendGuard) expire(generation uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.timedOut || g.timer == nil || generation != g.generation {
		return
	}
	g.elapsed = time.Since(g.started)
	g.timedOut = true
	g.cancel(&RequestSendTimeoutError{Budget: g.budget})
}

func (g *requestSendGuard) stopLocked() {
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
		g.generation++ // A callback already queued by AfterFunc must be inert.
		if !g.timedOut {
			g.elapsed = time.Since(g.started)
		}
	}
}

func (g *requestSendGuard) written(round uint64, info httptrace.WroteRequestInfo) {
	if info.Err != nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if round != g.round {
		return
	}
	g.stopLocked()
}

func (g *requestSendGuard) finish() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.stopLocked()
	return g.elapsed
}

type requestSendTransport struct {
	base  http.RoundTripper
	guard *requestSendGuard
}

func (t requestSendTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	g := t.guard
	round := g.begin()
	g.arm(round)
	ctx := httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GetConn:      func(string) { g.arm(round) },
		WroteRequest: func(info httptrace.WroteRequestInfo) { g.written(round, info) },
	})
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	// An early final response is also a handoff to response/body handling.
	// Do not close the guard yet: Client may follow a redirect next.
	g.mu.Lock()
	g.stopLocked()
	g.mu.Unlock()
	return resp, err
}

// releaseResponseBody unregisters the caller's cancellation once streaming has
// finished. It never cancels: on HTTP/2 that races the stream's teardown, and
// the next request can find the pooled connection not yet idle.
type releaseResponseBody struct {
	io.ReadCloser
	release func() bool
	once    sync.Once
}

func (b *releaseResponseBody) done() { b.once.Do(func() { b.release() }) }
func (b *releaseResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.done()
	}
	return n, err
}
func (b *releaseResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.done()
	return err
}
