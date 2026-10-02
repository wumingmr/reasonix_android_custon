package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

type opaqueTransportWrapper struct{ cause error }

func (opaqueTransportWrapper) Error() string   { panic("classification must not read error wording") }
func (e opaqueTransportWrapper) Unwrap() error { return e.cause }

type http2ConnectionError uint32

func (http2ConnectionError) Error() string { return "connection error: PROTOCOL_ERROR" }

func TestHTTP2ClassificationUsesTypeAndCode(t *testing.T) {
	for _, cause := range []error{
		http2.ConnectionError(http2.ErrCodeFrameSize),
		http2.StreamError{StreamID: 1, Code: http2.ErrCodeFrameSize},
		http2.GoAwayError{ErrCode: http2.ErrCodeFrameSize},
		new(http2.ConnectionError(http2.ErrCodeFrameSize)),
		&http2.StreamError{StreamID: 1, Code: http2.ErrCodeFrameSize},
		&http2.GoAwayError{ErrCode: http2.ErrCodeFrameSize},
	} {
		if got := HTTP2TransportCode(opaqueTransportWrapper{errors.Join(errors.New("unrelated"), cause)}); got != "FRAME_SIZE_ERROR" {
			t.Fatalf("typed code = %q", got)
		}
	}
	for _, cause := range []error{http2ConnectionError(1), errors.New("connection error: PROTOCOL_ERROR"), http2.ConnectionError(999)} {
		if got := HTTP2TransportCode(opaqueTransportWrapper{cause}); got != "" {
			t.Fatalf("classified foreign type, wording, or unknown code: %q", got)
		}
	}
}

func TestHTTP2StdlibStreamAndGoAwayTypes(t *testing.T) {
	for _, goAway := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "goaway"}[goAway], func(t *testing.T) {
			code := http2.ErrCodeFrameSize
			if goAway {
				code = http2.ErrCodeProtocol
			}
			server := httptest.NewUnstartedServer(http.NotFoundHandler())
			server.EnableHTTP2 = true
			server.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
				"h2": func(_ *http.Server, conn *tls.Conn, _ http.Handler) {
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err := io.ReadFull(conn, make([]byte, len(http2.ClientPreface))); err != nil {
						return
					}
					framer := http2.NewFramer(conn, conn)
					if err := framer.WriteSettings(); err != nil {
						return
					}
					for {
						frame, err := framer.ReadFrame()
						if err != nil {
							return
						}
						if headers, ok := frame.(*http2.HeadersFrame); ok {
							if goAway {
								_ = framer.WriteGoAway(headers.StreamID, code, nil)
							} else {
								// A peer PROTOCOL_ERROR triggers net/http's internal retry;
								// use a non-retryable code to inspect the private stream type.
								_ = framer.WriteRSTStream(headers.StreamID, code)
							}
							// Drain late client frames before closing: a Windows close
							// with unread inbound data sends RST, clobbering the frame
							// just written (upstream #11152 flake). Exits early on EOF.
							_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
							for {
								if _, err := framer.ReadFrame(); err != nil {
									break
								}
							}
							return
						}
					}
				},
			}
			server.StartTLS()
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(req)
			if response != nil {
				response.Body.Close()
			}
			if got := HTTP2TransportCode(opaqueTransportWrapper{err}); got != code.String() {
				t.Fatalf("code=%q, err=%v", got, err)
			}
		})
	}
}
