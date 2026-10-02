package provider

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptrace"
)

func (s *requestObservationState) request(req *http.Request) {
	if s == nil || req == nil || req.URL == nil {
		return
	}
	s.update("request_built", func(v *RequestObservation) {
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
		DNSStart: func(httptrace.DNSStartInfo) { s.update("dns_started", nil) },
		ConnectStart: func(network, addr string) {
			s.update("connect_started", func(v *RequestObservation) {
				v.Network = network
				if len(addr) <= 255 {
					v.DialAddress = addr
				}
			})
		},
		TLSHandshakeStart: func() { s.update("tls_started", nil) },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if err == nil {
				s.update("tls_complete", func(v *RequestObservation) {
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
			s.update("connection_acquired", func(v *RequestObservation) {
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
				s.update("request_written", func(v *RequestObservation) { v.WrittenAt = v.ObservedAt })
			}
		},
	})
}
