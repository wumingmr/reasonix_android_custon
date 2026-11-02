package provider

import (
	"context"
	"errors"
	"io"
	"net"
)

// transportErrorCode never includes error prose, URLs, headers or bodies.
// EOF means failure only when the caller is reporting an unsuccessful request
// or body read; a normal completed response must not pass its EOF here.
func transportErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.As(err, new(*RequestSendTimeoutError)):
		return "request_send_timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case HTTP2TransportCode(err) != "":
		return "http2_protocol"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, net.ErrClosed):
		return "connection_closed"
	}
	for _, closed := range closedConnectionErrnos {
		if errors.Is(err, closed) {
			return "connection_reset"
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_error"
	}
	return "unknown"
}

func transportFailureKind(err error) string {
	switch transportErrorCode(err) {
	case "request_send_timeout":
		return "request_send_timeout"
	case "eof", "unexpected_eof":
		return "transport_eof"
	case "connection_reset", "connection_closed":
		return "transport_reset"
	case "timeout":
		return "transport_timeout"
	case "dns_error":
		return "transport_dns"
	}
	return ""
}

func requestErrorPhase(phase string) string {
	switch phase {
	case "dns_started", "dns_failed":
		return "dns"
	case "connect_started", "connect_failed":
		return "connect"
	case "tls_started", "tls_failed":
		return "tls"
	case "connection_acquired", "request_write_failed":
		return "request_write"
	case "request_written":
		return "response_headers"
	case "headers_received", "body_received":
		return "response_body"
	default:
		return "unknown"
	}
}
