package provider

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptrace"
	"time"
)

func (s *requestObservationState) request(req *http.Request) {
	if s == nil || req == nil || req.URL == nil {
		return
	}
	s.updateBeforeHeaders("request_built", func(v *RequestObservation) {
		v.Method, v.Host, v.RequestPath = req.Method, req.URL.Host, req.URL.EscapedPath()
		if len(v.Host) > 255 {
			v.Host = ""
		}
		if len(v.RequestPath) > 512 {
			v.RequestPath = ""
		}
		v.RequestBytes = req.ContentLength
	})
}

func (s *requestObservationState) traceContext(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) {
			s.updateBeforeHeaders("connection_wait", func(v *RequestObservation) {
				// Redirects and Transport retries start another send stage. A prior
				// write is not evidence that this stage's body has been sent.
				v.WrittenAt = time.Time{}
				v.ErrorCode, v.ErrorPhase = "", ""
			})
		},
		DNSStart: func(httptrace.DNSStartInfo) { s.updateBeforeHeaders("dns_started", nil) },
		DNSDone:  func(info httptrace.DNSDoneInfo) { s.transportFailure(info.Err, "dns_failed") },
		ConnectStart: func(network, addr string) {
			s.updateBeforeHeaders("connect_started", func(v *RequestObservation) {
				v.Network = network
				if len(addr) <= 255 {
					v.DialAddress = addr
				}
			})
		},
		TLSHandshakeStart: func() { s.updateBeforeHeaders("tls_started", nil) },
		ConnectDone:       func(_, _ string, err error) { s.transportFailure(err, "connect_failed") },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if err != nil {
				s.transportFailure(err, "tls_failed")
			}
			if err == nil {
				s.updateBeforeHeaders("tls_complete", func(v *RequestObservation) {
					if state.NegotiatedProtocol == "h2" {
						v.HTTPProtocol = "HTTP/2.0"
					}
					if state.NegotiatedProtocol == "http/1.1" {
						v.HTTPProtocol = "HTTP/1.1"
					}
				})
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			s.updateBeforeHeaders("connection_acquired", func(v *RequestObservation) {
				// A successful connection supersedes failed DNS/dial candidates.
				v.ErrorCode, v.ErrorPhase = "", ""
				v.ConnectedAt, v.ConnectionReused = v.ObservedAt, info.Reused
				if info.Conn != nil && info.Conn.RemoteAddr() != nil {
					if addr := info.Conn.RemoteAddr().String(); len(addr) <= 255 {
						v.RemoteAddress = addr
					}
				}
				if conn, ok := info.Conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
					if conn.ConnectionState().NegotiatedProtocol == "h2" {
						v.HTTPProtocol = "HTTP/2.0"
					}
				}
			})
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				s.updateBeforeHeaders("request_written", func(v *RequestObservation) {
					v.WrittenAt = v.ObservedAt
					v.ErrorCode, v.ErrorPhase = "", ""
				})
			} else {
				s.transportFailure(info.Err, "request_write_failed")
			}
		},
	})
}
