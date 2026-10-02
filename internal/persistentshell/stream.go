package persistentshell

import (
	"io"
	"strings"

	"reasonix/internal/shellrun"
)

// maxIncompleteEscape bounds the escape-sequence fragment carried between PTY
// reads. A stream that never terminates a sequence is malformed, not a reason
// to grow memory.
const maxIncompleteEscape = 8 << 10

// sanitizer turns raw PTY bytes into model-visible text. The command now runs
// on a real terminal, so programs that probe isatty emit colour and cursor
// control that the one-shot path never produced; those sequences are dropped
// rather than shown. Incomplete sequences and a trailing carriage return carry
// into the next read.
type sanitizer struct {
	pending []byte
	carryCR bool
}

func (s *sanitizer) push(chunk []byte) string {
	if len(chunk) == 0 {
		return ""
	}
	s.pending = append(s.pending, chunk...)
	text, rest := stripTerminalControls(s.pending)
	if len(rest) > maxIncompleteEscape {
		rest = nil
	}
	s.pending = append(s.pending[:0], rest...)
	return s.lineEndings(text)
}

// flush returns any carried text once the PTY is done producing bytes.
func (s *sanitizer) flush() string {
	s.pending = nil
	if !s.carryCR {
		return ""
	}
	s.carryCR = false
	return "\n"
}

func (s *sanitizer) lineEndings(text string) string {
	if s.carryCR {
		text = "\r" + text
		s.carryCR = false
	}
	// A trailing carriage return cannot be classified yet: the next read decides
	// whether it precedes a newline (one line break) or stands alone.
	for strings.HasSuffix(text, "\r") {
		text = text[:len(text)-1]
		s.carryCR = true
	}
	return normalizePTY(text)
}

// stripTerminalControls removes CSI/OSC/two-byte escape sequences and BEL,
// returning the printable text plus any trailing incomplete sequence.
func stripTerminalControls(b []byte) (string, []byte) {
	if idx := indexControl(b); idx < 0 {
		return string(b), nil
	}
	var out strings.Builder
	out.Grow(len(b))
	i := 0
	for i < len(b) {
		c := b[i]
		if c == 0x07 { // BEL is a notification, not text
			i++
			continue
		}
		if c != 0x1b {
			out.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(b) {
			return out.String(), b[i:]
		}
		switch b[i+1] {
		case '[':
			end := i + 2
			for end < len(b) && (b[end] < 0x40 || b[end] > 0x7e) {
				end++
			}
			if end >= len(b) {
				return out.String(), b[i:]
			}
			i = end + 1
		case ']':
			end, ok := indexOSCTerminator(b, i+2)
			if !ok {
				return out.String(), b[i:]
			}
			i = end
		default:
			i += 2
		}
	}
	return out.String(), nil
}

func indexControl(b []byte) int {
	for i, c := range b {
		if c == 0x1b || c == 0x07 {
			return i
		}
	}
	return -1
}

func indexOSCTerminator(b []byte, from int) (int, bool) {
	for i := from; i < len(b); i++ {
		if b[i] == 0x07 {
			return i + 1, true
		}
		if b[i] == 0x1b {
			if i+1 >= len(b) {
				return 0, false
			}
			if b[i+1] == '\\' {
				return i + 2, true
			}
		}
	}
	return 0, false
}

// capture extracts one command's body from the sanitized stream. It scans only
// newly arrived text plus a marker-width overlap, so a command that prints
// megabytes costs time linear in its output instead of rescanning the whole
// transcript on every read.
type capture struct {
	start, end string
	out        *shellrun.BoundedOutput
	progress   io.Writer

	started  bool
	done     bool
	exitCode int

	pre  string // scratch while waiting for the start marker
	hold string // post-start text withheld until it cannot be a marker prefix
}

func newCapture(start, end string, progress io.Writer) *capture {
	return &capture{start: start, end: end, out: shellrun.NewBoundedOutput(), progress: progress}
}

func (c *capture) push(text string) {
	if text == "" || c.done {
		return
	}
	if !c.started {
		c.pre += text
		marker := c.start + "\n"
		idx := strings.Index(c.pre, marker)
		if idx < 0 {
			if len(c.pre) > preStartCap {
				c.pre = c.pre[len(c.pre)-preStartCap:]
			}
			return
		}
		rest := c.pre[idx+len(marker):]
		c.started, c.pre = true, ""
		c.consume(rest)
		return
	}
	c.consume(text)
}

func (c *capture) consume(text string) {
	c.hold += text
	for {
		idx := strings.Index(c.hold, c.end)
		if idx < 0 {
			break
		}
		status, ok, pending := parseStatus(c.hold[idx+len(c.end):])
		if pending {
			// The status line has not arrived yet; withhold from the marker on.
			c.emit(c.hold[:idx])
			c.hold = c.hold[idx:]
			return
		}
		if !ok {
			// A complete trailer that is not digits is not this command's
			// completion (a shell tracing its own input prints one). Treat it
			// as output and keep scanning.
			c.emit(c.hold[:idx+len(c.end)])
			c.hold = c.hold[idx+len(c.end):]
			continue
		}
		c.emit(c.hold[:idx])
		c.hold, c.done, c.exitCode = "", true, status
		return
	}
	if len(c.hold) > markerOverlap {
		keep := len(c.hold) - markerOverlap
		c.emit(c.hold[:keep])
		c.hold = c.hold[keep:]
	}
}

func (c *capture) emit(text string) {
	if text == "" {
		return
	}
	c.out.WriteString(text)
	if c.progress != nil {
		_, _ = io.WriteString(c.progress, text)
	}
}

// partial releases text still withheld for marker matching. A command that
// timed out or died never printed its status marker, and its output is the only
// evidence the model gets about what ran.
func (c *capture) partial() string {
	if c.hold != "" {
		c.emit(c.hold)
		c.hold = ""
	}
	return c.out.String()
}

func (c *capture) body() string { return c.out.String() }
