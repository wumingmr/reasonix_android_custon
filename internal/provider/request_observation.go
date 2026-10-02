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
	ID               uint64    `json:"id"`
	StartedAt        time.Time `json:"startedAt"`
	ObservedAt       time.Time `json:"observedAt"`
	Phase            string    `json:"phase"`
	LastPhase        string    `json:"lastPhase,omitempty"`
	ConnectedAt      time.Time `json:"connectedAt,omitempty"`
	WrittenAt        time.Time `json:"writtenAt,omitempty"`
	HeadersAt        time.Time `json:"headersAt,omitempty"`
	FirstBodyAt      time.Time `json:"firstBodyAt,omitempty"`
	LastBodyAt       time.Time `json:"lastBodyAt,omitempty"`
	FinishedAt       time.Time `json:"finishedAt,omitempty"`
	Status           int       `json:"status,omitempty"`
	BodyBytes        int64     `json:"bodyBytes"`
	Method           string    `json:"method,omitempty"`
	Host             string    `json:"host,omitempty"`
	RequestPath      string    `json:"requestPath,omitempty"`
	RequestBytes     int64     `json:"requestBytes,omitempty"`
	Network          string    `json:"network,omitempty"`
	DialAddress      string    `json:"dialAddress,omitempty"`
	RemoteAddress    string    `json:"remoteAddress,omitempty"`
	HTTPProtocol     string    `json:"httpProtocol,omitempty"`
	ConnectionReused bool      `json:"connectionReused,omitempty"`
	TransportCode    string    `json:"transportCode,omitempty"`
}

type requestObserverKey struct{}

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
	return s.traceContext(ctx), s
}

func (s *requestObservationState) update(phase string, edit func(*RequestObservation)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.value.FinishedAt.IsZero() {
		return
	}
	s.value.ObservedAt = time.Now().UTC()
	previousPhase := s.value.Phase
	s.value.Phase = phase
	if edit != nil {
		edit(&s.value)
	}
	if !s.value.FinishedAt.IsZero() {
		s.value.LastPhase = previousPhase
	}
	s.observe(s.value)
}

func (s *requestObservationState) finish(err error, phase string) {
	if s == nil {
		return
	}
	if errors.Is(s.ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		phase = "canceled"
	} else if errors.Is(s.ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		phase = "deadline_exceeded"
	}
	s.update(phase, func(v *RequestObservation) {
		v.FinishedAt = v.ObservedAt
		v.TransportCode = HTTP2TransportCode(err)
	})
}

func (s *requestObservationState) response(resp *http.Response) {
	if s == nil {
		return
	}
	s.update("headers_received", func(v *RequestObservation) {
		v.HeadersAt, v.Status, v.HTTPProtocol = v.ObservedAt, resp.StatusCode, resp.Proto
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
