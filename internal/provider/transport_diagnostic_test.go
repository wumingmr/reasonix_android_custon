package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestHTTP2FailureClassificationDoesNotEnableRetries(t *testing.T) {
	for _, cause := range []error{
		http2.ConnectionError(http2.ErrCodeProtocol),
		http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol},
		http2.GoAwayError{LastStreamID: 3, ErrCode: http2.ErrCodeProtocol, DebugData: "private"},
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, cause })}
			_, err := SendWithRetry(t.Context(), client, SendOptions{Provider: "test", Protocol: "openai"}, newDummyReq)
			diagnostic := DiagnoseFailure(fmt.Errorf("outer: %w", err))
			if calls != 1 || diagnostic.Kind != FailureKindTransportProtocol || diagnostic.TransportCode != "PROTOCOL_ERROR" || !errors.Is(err, cause) {
				t.Fatalf("calls=%d diagnostic=%+v err=%v", calls, diagnostic, err)
			}
			if ClassifyRecovery(err).Retryable {
				t.Fatal("classification changed the retry policy")
			}
		})
	}
	for _, cause := range []error{
		errors.New("connection error: PROTOCOL_ERROR"),
		errors.New("stream error: stream ID 3; PROTOCOL_ERROR"),
		errors.New("http2: server sent GOAWAY and closed the connection; LastStreamID=3, ErrCode=PROTOCOL_ERROR"),
		errors.New("invalid header field value: PROTOCOL_ERROR"),
		&APIError{Body: "connection error: PROTOCOL_ERROR", Status: 400},
		fmt.Errorf("connection error: PROTOCOL_ERROR: %w", &APIError{Status: 400}),
		context.Canceled,
	} {
		if got := DiagnoseFailure(&url.Error{Op: "Post", URL: "https://example.test", Err: cause}); got.Kind == FailureKindTransportProtocol {
			t.Fatalf("misclassified caller/provider error: %+v", got)
		}
	}
}

func TestHTTP2StdlibProtocolFailureRecordsNegotiatedTransport(t *testing.T) {
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	server.EnableHTTP2 = true
	server.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
		"h2": func(_ *http.Server, conn *tls.Conn, _ http.Handler) {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(conn, make([]byte, len(http2.ClientPreface))); err != nil {
				return
			}
			// A valid initial SETTINGS frame, followed by an illegal DATA frame on
			// stream zero, deterministically exercises net/http's private error type.
			_, _ = conn.Write([]byte{0, 0, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
			_, _ = io.Copy(io.Discard, conn)
		},
	}
	server.StartTLS()
	defer server.Close()
	var mu sync.Mutex
	var last RequestObservation
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = WithRequestObserver(ctx, func(v RequestObservation) { mu.Lock(); last = v; mu.Unlock() })
	_, err := SendWithRetry(ctx, server.Client(), SendOptions{}, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions?credential=private-query", nil)
	})
	if err == nil || DiagnoseFailure(err).Kind != FailureKindTransportProtocol {
		t.Fatalf("stdlib error: %T %v", err, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if last.TransportCode != "PROTOCOL_ERROR" || last.HTTPProtocol != "HTTP/2.0" || last.RemoteAddress == "" || last.Phase != "request_error" || last.ConnectedAt.IsZero() || !last.HeadersAt.IsZero() {
		t.Fatalf("lost negotiated transport evidence: %+v", last)
	}
}
