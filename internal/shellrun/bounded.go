package shellrun

import "io"

// The foreground output caps are one contract shared by every shell execution
// path. They live here so a second runner cannot ship a different memory bound
// than the one #6473/#6528 established.

// BoundedOutput collects model-visible combined output under the shared cap:
// complete text up to 10 MiB, then a fixed head plus a rolling 64 KiB tail
// separated by an explicit truncation notice. Write never short-writes.
type BoundedOutput struct {
	buf *boundedBuffer
}

// NewBoundedOutput returns a collector using the shared foreground caps.
func NewBoundedOutput() *BoundedOutput {
	c := &outputCollector{}
	c.combined = &boundedBuffer{
		mu:        &c.mu,
		limit:     combinedOutputMaxBytes,
		tailLimit: combinedOutputTailBytes,
		marker:    combinedOutputTruncated,
	}
	return &BoundedOutput{buf: c.combined}
}

// Write appends p, evicting middle output once the cap is crossed.
func (o *BoundedOutput) Write(p []byte) (int, error) { return o.buf.Write(p) }

// WriteString appends s.
func (o *BoundedOutput) WriteString(s string) {
	if s == "" {
		return
	}
	_, _ = o.buf.Write([]byte(s))
}

// String returns the collected output, including the truncation notice when the
// cap was crossed.
func (o *BoundedOutput) String() string { return o.buf.String() }

// Truncated reports whether output was evicted.
func (o *BoundedOutput) Truncated() bool {
	o.buf.mu.Lock()
	defer o.buf.mu.Unlock()
	return o.buf.truncated
}

// ProgressWriter buffers partial UTF-8 characters until its owner ends the stream.
type ProgressWriter interface {
	io.Writer
	Flush()
}

// NewProgressWriter returns the shared live-progress sink. Live output crosses
// async UI queues and append-only reducers before the bounded final result
// replaces it, so it is capped far below the final output cap; a never-ending
// command must not exhaust memory on the transient path.
func NewProgressWriter(emit func(string)) ProgressWriter {
	return newProgressWriter(emit, progressOutputMaxBytes, progressOutputTruncated)
}
