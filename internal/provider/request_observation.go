package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// RequestObservation is content-free, process-local transport evidence. Body
// bytes include SSE comments/heartbeats, not just model output. It deliberately
// excludes URL userinfo/query/fragment, headers and request/response bodies.
type RequestObservation struct {
	ID                     uint64    `json:"id"`
	StartedAt              time.Time `json:"startedAt"`
	ObservedAt             time.Time `json:"observedAt"`
	Phase                  string    `json:"phase"`
	LastPhase              string    `json:"lastPhase,omitempty"`
	ConnectedAt            time.Time `json:"connectedAt,omitempty"`
	WrittenAt              time.Time `json:"writtenAt,omitempty"`
	HeadersAt              time.Time `json:"headersAt,omitempty"`
	FirstBodyAt            time.Time `json:"firstBodyAt,omitempty"`
	LastBodyAt             time.Time `json:"lastBodyAt,omitempty"`
	FinishedAt             time.Time `json:"finishedAt,omitempty"`
	Status                 int       `json:"status,omitempty"`
	BodyBytes              int64     `json:"bodyBytes"`
	Method                 string    `json:"method,omitempty"`
	Host                   string    `json:"host,omitempty"`
	RequestPath            string    `json:"requestPath,omitempty"`
	RequestBytes           int64     `json:"requestBytes,omitempty"`
	Network                string    `json:"network,omitempty"`
	DialAddress            string    `json:"dialAddress,omitempty"`
	RemoteAddress          string    `json:"remoteAddress,omitempty"`
	HTTPProtocol           string    `json:"httpProtocol,omitempty"`
	ConnectionReused       bool      `json:"connectionReused,omitempty"`
	TransportCode          string    `json:"transportCode,omitempty"`
	ProtocolFallbackReason string    `json:"protocolFallbackReason,omitempty"`
	ErrorCode              string    `json:"errorCode,omitempty"`
	ErrorPhase             string    `json:"errorPhase,omitempty"`
	TimeoutSource          string    `json:"timeoutSource,omitempty"`
	SendTimeoutMs          int64     `json:"sendTimeoutMs,omitempty"`
	SendElapsedMs          int64     `json:"sendElapsedMs,omitempty"`
}

type requestObserverKey struct{}
type protocolObservationKey struct{}

var observedRequestID atomic.Uint64

// WithRequestObserver attaches a synchronous, concurrency-safe observer. The
// callback must be short and must not call back into the observed transport.
// It changes no provider-visible bytes or timeout/retry behavior.
func WithRequestObserver(ctx context.Context, observe func(RequestObservation)) context.Context {
	return context.WithValue(ctx, requestObserverKey{}, observe)
}

type requestObservationState struct {
	mu      sync.Mutex
	value   RequestObservation
	observe func(RequestObservation)
	ctx     context.Context
}

func observeRequest(ctx context.Context) (context.Context, *requestObservationState) {
	observe, _ := ctx.Value(requestObserverKey{}).(func(RequestObservation))
	if observe == nil {
		return ctx, nil
	}
	now := time.Now().UTC()
	s := &requestObservationState{ctx: ctx, observe: observe, value: RequestObservation{ID: observedRequestID.Add(1), StartedAt: now}}
	s.update("request_started", nil)
	return s.traceContext(context.WithValue(ctx, protocolObservationKey{}, s)), s
}

func (s *requestObservationState) update(phase string, edit func(*RequestObservation)) {
	s.updatePhase(phase, false, edit)
}

func (s *requestObservationState) updateBeforeHeaders(phase string, edit func(*RequestObservation)) {
	s.updatePhase(phase, true, edit)
}

func (s *requestObservationState) updatePhase(phase string, beforeHeaders bool, edit func(*RequestObservation)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.value.FinishedAt.IsZero() || (beforeHeaders && !s.value.HeadersAt.IsZero()) {
		return
	}
	s.value.ObservedAt = time.Now().UTC()
	previousPhase := s.value.Phase
	if edit != nil {
		edit(&s.value)
	}
	s.value.Phase = phase
	if !s.value.FinishedAt.IsZero() {
		s.value.LastPhase = previousPhase
	}
	s.observe(s.value)
}

func (s *requestObservationState) finish(err error, phase string) {
	if s == nil {
		return
	}
	// A concurrent body read can report EOF/reset after caller cancellation.
	// Keep its diagnostic cause consistent with the caller-owned terminal state.
	if parentErr := s.ctx.Err(); parentErr != nil {
		err = parentErr
	}
	if errors.Is(s.ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		phase = "canceled"
	} else if errors.Is(s.ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		phase = "deadline_exceeded"
	}
	s.update(phase, func(v *RequestObservation) {
		v.FinishedAt = v.ObservedAt
		v.TransportCode = HTTP2TransportCode(err)
		if err != nil && phase != "body_eof" {
			v.ErrorCode = transportErrorCode(err)
			if v.ErrorPhase == "" {
				// LastPhase is assigned by update after this edit.
				v.ErrorPhase = requestErrorPhase(v.Phase)
			}
			if v.ErrorCode == "request_send_timeout" {
				v.TimeoutSource = "send_guard"
			} else if v.ErrorCode == "timeout" {
				v.TimeoutSource = "transport"
				if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
					v.TimeoutSource = "parent_context"
				} else {
					// A nested context deadline has no known owner. Do not use
					// errors.Is here: Transport's own header timeout also matches
					// DeadlineExceeded, but is a known transport timeout.
					for cause := err; cause != nil; cause = errors.Unwrap(cause) {
						if cause == context.DeadlineExceeded { //nolint:errorlint // identity walk; errors.Is also matches Transport's own timeout
							v.TimeoutSource = "unknown"
							break
						}
					}
				}
			}
		}
	})
}

func (s *requestObservationState) sendTiming(budget, elapsed time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value.SendTimeoutMs, s.value.SendElapsedMs = budget.Milliseconds(), elapsed.Milliseconds()
}

func (s *requestObservationState) transportFailure(err error, phase string) {
	if err == nil {
		return
	}
	s.updateBeforeHeaders(phase, func(v *RequestObservation) {
		v.ErrorCode, v.ErrorPhase = transportErrorCode(err), requestErrorPhase(phase)
	})
}

func (s *requestObservationState) response(resp *http.Response) {
	if s == nil {
		return
	}
	s.update("headers_received", func(v *RequestObservation) {
		v.HeadersAt, v.Status, v.HTTPProtocol = v.ObservedAt, resp.StatusCode, resp.Proto
		v.ErrorCode, v.ErrorPhase = "", ""
	})
	resp.Body = &observedResponseBody{ReadCloser: resp.Body, state: s}
}

type observedResponseBody struct {
	io.ReadCloser
	state *requestObservationState
}

func (b *observedResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.state.update("body_received", func(v *RequestObservation) {
			if v.FirstBodyAt.IsZero() {
				v.FirstBodyAt = v.ObservedAt
			}
			v.LastBodyAt = v.ObservedAt
			v.BodyBytes += int64(n)
		})
	}
	if err != nil {
		phase := "body_error"
		if errors.Is(err, io.EOF) {
			phase = "body_eof"
		}
		b.state.finish(err, phase)
	}
	return n, err
}

func (b *observedResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.state.finish(err, "body_closed")
	return err
}
