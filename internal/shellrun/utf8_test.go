package shellrun

import (
	"context"
	"encoding/json"
	"os/exec"
	"reasonix/internal/proc"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestForegroundFlushesUTF8OnCompletionAndCancel(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		var chunks []string
		ctx, cancel := context.WithCancel(context.Background())
		RunForeground(ctx, Request{Argv: []string{"fixture"}, Progress: jsonProgressCollector(t, &chunks),
			Run: func(_ context.Context, cmd *exec.Cmd, _ proc.RunOptions) (*proc.TrackedCommand, error) {
				_, _ = cmd.Stdout.Write([]byte{'A', 0xe4, 0xb8})
				if canceled {
					cancel()
					return nil, context.Canceled
				}
				return nil, nil
			},
		})
		cancel()
		if got := strings.Join(chunks, ""); got != "A\ufffd" {
			t.Fatalf("cancel=%v got %q", canceled, got)
		}
	}
}

func jsonProgressCollector(t *testing.T, chunks *[]string) func(string) {
	t.Helper()
	return func(s string) {
		t.Helper()
		if !utf8.ValidString(s) {
			t.Errorf("progress event contains incomplete UTF-8: %x", []byte(s))
		}
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var decoded string
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		*chunks = append(*chunks, decoded)
	}
}

func TestProgressWriterUTF8SplitJSON(t *testing.T) {
	const source = "中文路径/😀.txt\n"
	for split := 1; split < len(source); split++ {
		var chunks []string
		w := newProgressWriter(jsonProgressCollector(t, &chunks), 1024, "[truncated]")
		for _, p := range []string{source[:split], source[split:]} {
			if n, err := w.Write([]byte(p)); err != nil || n != len(p) {
				t.Fatalf("split=%d Write=%d,%v", split, n, err)
			}
		}
		if got := strings.Join(chunks, ""); got != source {
			t.Errorf("split=%d JSON roundtrip=%q, want %q", split, got, source)
		}
	}
}

func TestProgressWriterUTF8ByteCap(t *testing.T) {
	const source = "中😀文"
	for limit := 1; limit <= len(source); limit++ {
		var chunks []string
		w := newProgressWriter(jsonProgressCollector(t, &chunks), limit, "[truncated]")
		for i := range len(source) {
			_, _ = w.Write([]byte(source[i : i+1]))
		}
		end := limit
		for !utf8.ValidString(source[:end]) {
			end--
		}
		want := source[:end]
		if limit < len(source) {
			want += "[truncated]"
		}
		if got := strings.Join(chunks, ""); got != want {
			t.Errorf("limit=%d got %q, want %q", limit, got, want)
		}
	}
}

// The owner flushes when a stream ends, including early termination. A partial
// character cannot be recovered then, but must not disappear or corrupt JSON.
func TestProgressWriterUTF8FlushPartial(t *testing.T) {
	var chunks []string
	w := newProgressWriter(jsonProgressCollector(t, &chunks), 1024, "[truncated]")
	_, _ = w.Write([]byte{'A', 0xe4, 0xb8})
	if got := strings.Join(chunks, ""); got != "A" {
		t.Errorf("incomplete character emitted before flush: %q", got)
	}
	flusher, ok := any(w).(interface{ Flush() })
	if !ok {
		t.Fatal("progressWriter must expose Flush for stream EOF and cancellation")
	}
	flusher.Flush()
	flusher.Flush()
	if got := strings.Join(chunks, ""); got != "A\ufffd" {
		t.Errorf("idempotent final flush=%q, want one replacement for unfinished rune", got)
	}
}

func TestBoundedBufferUTF8HeadTail(t *testing.T) {
	const source = "头😀中间内容尾😀"
	for split := 1; split < len(source); split++ {
		b := &boundedBuffer{mu: &sync.Mutex{}, limit: 16, tailLimit: 5, marker: "..."}
		_, _ = b.Write([]byte(source[:split]))
		_, _ = b.Write([]byte(source[split:]))
		got := b.String()
		if !utf8.ValidString(got) || len(got) > b.limit {
			t.Errorf("split=%d invalid or over cap: %q (%x)", split, got, []byte(got))
		}
		if got != "头😀...😀" {
			t.Errorf("split=%d got %q, want complete head/tail runes", split, got)
		}
	}
}
