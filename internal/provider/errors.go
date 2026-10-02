package provider

import "errors"

// ErrEmptyResponse marks a clean provider completion that carried no text,
// reasoning, tool calls, response items, or server-side activity. The agent
// may safely retry the frozen request because the empty attempt produced no
// assistant message that can be committed to conversation history.
var ErrEmptyResponse = errors.New("empty provider response")

// ErrNonStreamingResponse marks a stream request answered with a complete
// non-SSE body (a gateway landing page, an HTML or JSON error page). It is an
// endpoint configuration problem, not a dropped connection, so it never wraps
// io.ErrUnexpectedEOF and retrying the same request cannot recover it.
var ErrNonStreamingResponse = errors.New("endpoint returned a non-streaming response")
