package persistentshell

import (
	"strings"
	"testing"
)

func TestSanitizerStripsTerminalControls(t *testing.T) {
	var s sanitizer
	got := s.push([]byte("\x1b[31mred\x1b[0m done\x1b]0;title\x07!"))
	if got != "red done!" {
		t.Fatalf("got %q", got)
	}
}

// An escape sequence split across two PTY reads must not leak its bytes.
func TestSanitizerCarriesSplitEscape(t *testing.T) {
	var s sanitizer
	first := s.push([]byte("a\x1b[3"))
	second := s.push([]byte("1mb"))
	if first+second != "ab" {
		t.Fatalf("got %q+%q", first, second)
	}
}

// A carriage return at a read boundary cannot be classified until the next
// byte arrives; classifying it early produced a spurious blank line.
func TestSanitizerCarriesTrailingCarriageReturn(t *testing.T) {
	var s sanitizer
	first := s.push([]byte("a\r"))
	second := s.push([]byte("\nb"))
	if first+second != "a\nb" {
		t.Fatalf("got %q+%q", first, second)
	}

	var lone sanitizer
	if got := lone.push([]byte("a\r")) + lone.push([]byte("b")); got != "a\nb" {
		t.Fatalf("lone CR: got %q", got)
	}
}

func TestCaptureExtractsBodyAndStatus(t *testing.T) {
	c := newCapture("S", "E:", nil)
	c.push("noise\nS\n")
	c.push("hello\n")
	c.push("E:0\n")
	if !c.done || c.exitCode != 0 {
		t.Fatalf("done=%v code=%d", c.done, c.exitCode)
	}
	if c.body() != "hello\n" {
		t.Fatalf("body=%q", c.body())
	}
}

// The end marker may arrive split across reads, and its status digits may
// arrive after the marker itself.
func TestCaptureHandlesSplitEndMarker(t *testing.T) {
	c := newCapture("S", "E:", nil)
	c.push("S\nbody")
	c.push("E")
	if c.done {
		t.Fatal("marker prefix alone must not complete")
	}
	c.push(":")
	if c.done {
		t.Fatal("marker without status must not complete")
	}
	c.push("12")
	if c.done {
		t.Fatal("status without terminator must not complete")
	}
	c.push("\n")
	if !c.done || c.exitCode != 12 {
		t.Fatalf("done=%v code=%d", c.done, c.exitCode)
	}
	if c.body() != "body" {
		t.Fatalf("body=%q", c.body())
	}
}

// Output with no trailing newline leaves the marker mid-line.
func TestCaptureBodyWithoutTrailingNewline(t *testing.T) {
	c := newCapture("S", "E:", nil)
	c.push("S\nhiE:0\n")
	if !c.done || c.body() != "hi" {
		t.Fatalf("done=%v body=%q", c.done, c.body())
	}
}

// A command that never reports a status still owes the model its output.
func TestCapturePartialReleasesWithheldText(t *testing.T) {
	c := newCapture("S", "E:", nil)
	c.push("S\npartial output")
	if c.body() == "partial output" {
		t.Fatal("tail must stay withheld until the command settles")
	}
	if got := c.partial(); got != "partial output" {
		t.Fatalf("partial=%q", got)
	}
}

// Large output must not be rescanned per read: the withheld window stays at
// marker width no matter how much text passed through.
func TestCaptureHoldStaysBounded(t *testing.T) {
	c := newCapture("S", "E:", nil)
	c.push("S\n")
	for range 100 {
		c.push(strings.Repeat("x", 4096))
	}
	if len(c.hold) > markerOverlap {
		t.Fatalf("hold grew to %d bytes", len(c.hold))
	}
	if len(c.body()) < 100*4096-markerOverlap {
		t.Fatalf("body lost data: %d bytes", len(c.body()))
	}
}

// A shell tracing its own input (set -x) prints the end marker followed by
// wrapper source instead of digits. That trailer is output, not a completion,
// and must not park the reader until the deadline.
func TestCaptureSkipsMarkerWithNonStatusTrailer(t *testing.T) {
	c := newCapture("S", "E:", nil)
	c.push("S\n+ printf '%s%s\\n' E:' \"$__rx_status\"\n")
	if c.done {
		t.Fatal("a traced marker must not complete the command")
	}
	c.push("real output\nE:3\n")
	if !c.done || c.exitCode != 3 {
		t.Fatalf("done=%v code=%d", c.done, c.exitCode)
	}
	if !strings.Contains(c.body(), "real output") {
		t.Fatalf("body=%q", c.body())
	}
}

// A CRLF host puts \r before the status terminator.
func TestCaptureAcceptsCarriageReturnStatus(t *testing.T) {
	c := newCapture("S", "E:", nil)
	c.push("S\nout\nE:5\r\n")
	if !c.done || c.exitCode != 5 {
		t.Fatalf("done=%v code=%d", c.done, c.exitCode)
	}
}
