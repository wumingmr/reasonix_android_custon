package persistentshell

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestExtractOutputIgnoresEchoedScript(t *testing.T) {
	start := "REASONIX_START_abc"
	end := "REASONIX_END_abc:"
	raw := posixCommandScript("pwd", start, end) +
		start + "\n" +
		"/tmp/work\n" +
		end + "0\n"
	body, code, ok := extractOutput(raw, start, end)
	if !ok {
		t.Fatal("expected completed extraction")
	}
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if body != "/tmp/work" {
		t.Fatalf("body=%q", body)
	}
}

func TestAnsiCQuoteASCIIByteRoundTrip(t *testing.T) {
	skipNonPOSIX(t)
	// NUL cannot occur in a shell argument. Include every other byte followed
	// by octal digits to catch escapes that accidentally consume the suffix.
	var input []byte
	for c := 1; c <= 255; c++ {
		input = append(input, byte(c), '7', '0')
	}
	quoted := ansiCQuote(string(input))
	for _, c := range []byte(quoted) {
		if c < 0x20 || c >= 0x7f {
			t.Fatalf("unsafe terminal input byte: %02x", c)
		}
	}
	for _, name := range []string{"bash", "zsh"} {
		t.Run(name, func(t *testing.T) {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s not installed", name)
			}
			cmd := exec.Command(path, "-c", "printf '%s' "+quoted)
			cmd.Env = []string{"LC_ALL=C"}
			got, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, input) {
				t.Fatalf("shell decoded %x, want %x", got, input)
			}
		})
	}
}

// The echoed wrapper source contains both markers. Completion must require the
// status digits that only the executed printf can produce.
func TestEchoedScriptAloneIsNotCompletion(t *testing.T) {
	start := "REASONIX_START_abc"
	end := "REASONIX_END_abc:"
	if _, _, ok := extractOutput(posixCommandScript("pwd", start, end), start, end); ok {
		t.Fatal("echoed wrapper source must not complete a command")
	}
	if readyLine(posixSetupScript()) {
		t.Fatal("echoed setup source must not report readiness")
	}
}

func TestExtractOutputCRLF(t *testing.T) {
	start := "REASONIX_START_x"
	end := "REASONIX_END_x:"
	raw := start + "\r\nhello\r\n" + end + "7\r\n"
	body, code, ok := extractOutput(raw, start, end)
	if !ok || code != 7 || body != "hello" {
		t.Fatalf("body=%q code=%d ok=%v", body, code, ok)
	}
}

// A command whose output has no trailing newline leaves the status marker
// mid-line. Requiring a line start there hung every such command until its
// deadline (printf without \n, echo -n, cat of a file with no final newline).
func TestExtractOutputWithoutTrailingNewline(t *testing.T) {
	start := "REASONIX_START_y"
	end := "REASONIX_END_y:"
	raw := start + "\nhi" + end + "0\n"
	body, code, ok := extractOutput(raw, start, end)
	if !ok || code != 0 || body != "hi" {
		t.Fatalf("body=%q code=%d ok=%v", body, code, ok)
	}
}

func TestReadyLine(t *testing.T) {
	if !readyLine("noise\n" + readyToken + "\nmore") {
		t.Fatal("ready token not detected")
	}
	if readyLine("printf '%s\\n' '" + readyToken + "'\n") {
		t.Fatal("quoted token in echoed source must not count as ready")
	}
}

func TestPosixQuote(t *testing.T) {
	if got := posixQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("got %q", got)
	}
}

// A multi-line command has to reach the shell as one physical input line, or an
// interactive shell prints PS2 and the wrapper's own source leaks into output.
func TestAnsiCQuoteKeepsOnePhysicalLine(t *testing.T) {
	script := posixCommandScript("cat <<'EOF'\nline\nEOF", "S", "E:")
	if strings.Count(script, "\n") != 1 || !strings.HasSuffix(script, "\n") {
		t.Fatalf("wrapper must be one line, got %q", script)
	}
	if got := ansiCQuote("a'b\nc\\d\te"); got != `$'a\'b\nc\\d\te'` {
		t.Fatalf("quote=%q", got)
	}
	if got := ansiCQuote("\x01"); got != `$'\001'` {
		t.Fatalf("control quote=%q", got)
	}
}

// The command's stdin is /dev/null, matching one-shot execution: a command that
// prompts fails immediately instead of blocking the session shell.
func TestCommandScriptClosesStdin(t *testing.T) {
	if !strings.Contains(posixCommandScript("read x", "S", "E:"), "</dev/null") {
		t.Fatal("wrapper must detach stdin")
	}
}

func TestLongCommandScriptBoundsPhysicalLinesAndPreservesState(t *testing.T) {
	skipNonPOSIX(t)
	text := strings.Repeat("中文😀'\\\n", 2000)
	stages := commandStages("value="+posixQuote(text)+"; printf '%s' \"$value\"; false", "S", "E:")
	var script, acknowledgements string
	for _, stage := range stages {
		script += stage.script
		if stage.ack != "" {
			acknowledgements += stage.ack + "\n"
		}
	}
	for line := range strings.SplitSeq(script, "\n") {
		if len(line) > 768 {
			t.Fatalf("physical input line has %d bytes; canonical PTYs can discard excess bytes", len(line))
		}
	}
	if _, _, ok := extractOutput(script, "S", "E:"); ok {
		t.Fatal("echoed multi-line source fabricated completion")
	}
	for _, name := range []string{"bash", "zsh"} {
		t.Run(name, func(t *testing.T) {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s not installed", name)
			}
			cmd := exec.CommandContext(t.Context(), path, "-c", script+"printf '%s' \"$value\"")
			cmd.Env = []string{"LC_ALL=C"}
			got, err := cmd.Output()
			want := acknowledgements + "S\n" + text + "E:1\n" + text
			if err != nil || string(got) != want {
				t.Fatalf("wrapper changed output, state or status: err=%v bytes=%d want=%d", err, len(got), len(want))
			}
		})
	}
}

// The line discipline can emit \r\r\n under output pressure. Mapping every \r
// to \n injected blank lines into model-visible output.
func TestNormalizePTYCollapsesCarriageReturnRuns(t *testing.T) {
	cases := map[string]string{
		"a\r\nb":     "a\nb",
		"a\r\r\nb":   "a\nb",
		"a\r\r\r\nb": "a\nb",
		"a\rb":       "a\nb",
		"plain":      "plain",
	}
	for in, want := range cases {
		if got := normalizePTY(in); got != want {
			t.Fatalf("normalizePTY(%q)=%q want %q", in, got, want)
		}
	}
}
