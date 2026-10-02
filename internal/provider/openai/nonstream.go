package openai

import (
	"fmt"
	"io"
	"strings"

	"reasonix/internal/provider"
)

// sseProbe watches the stream's lines so an incomplete stream can say whether
// the endpoint spoke SSE at all. A body with no `data:` line and a non-stream
// Content-Type is a gateway page or error document, not a dropped connection.
type sseProbe struct {
	sawData      bool
	firstNonData string // first non-blank non-`data:` line, clipped for the error text
}

// dataLine reports whether line is an SSE `data:` line, recording the first
// other non-blank line for the diagnostic.
func (p *sseProbe) dataLine(line string) bool {
	if line == "" {
		return false
	}
	if !strings.HasPrefix(line, "data:") {
		if p.firstNonData == "" {
			p.firstNonData = clipDiagnosticLine(line)
		}
		return false
	}
	p.sawData = true
	return true
}

// incompleteErr is the error for a stream that ended without a terminal event.
func (p *sseProbe) incompleteErr(name, contentType string) error {
	ct := strings.TrimSpace(contentType)
	if !p.sawData && !isEventStreamContentType(ct) {
		return fmt.Errorf("%s: stream ended before any SSE event (Content-Type %q, first line %q) — the endpoint returned a non-streaming response, e.g. a gateway landing page. Check that request_url/base_url points to a full API endpoint such as /v1/chat/completions or /v1/responses, not a gateway root: %w",
			name, ct, p.firstNonData, provider.ErrNonStreamingResponse)
	}
	return fmt.Errorf("%s: stream ended before completion: %w", name, io.ErrUnexpectedEOF)
}

// isEventStreamContentType reports whether a Content-Type claims a stream. An
// absent header counts as one: some gateways stream without it, so this only
// shapes the diagnostic and never rejects a response.
func isEventStreamContentType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	return ct == "" || strings.Contains(ct, "event-stream") || strings.Contains(ct, "ndjson")
}

// clipDiagnosticLine bounds a non-SSE line before it rides in an error
// message, so a multi-KB HTML page cannot bloat the surface text.
func clipDiagnosticLine(line string) string {
	const maxLen = 120
	if len(line) <= maxLen {
		return line
	}
	return line[:maxLen] + "…"
}
