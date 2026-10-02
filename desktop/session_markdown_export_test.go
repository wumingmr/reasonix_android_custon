package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/transcript"
)

func TestWriteSessionMarkdownStreamsCompleteRecords(t *testing.T) {
	var out bytes.Buffer
	messages := []HistoryMessage{
		{Role: "user", Content: "question"},
		{Role: "assistant", Reasoning: "thought", Content: "answer", ToolCalls: []transcript.ToolCall{{Name: "bash", Arguments: "echo ```"}}},
		{Role: "tool", Content: "done"},
	}
	if err := writeSessionMarkdown(&out, "demo\nsession", messages); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"# demo session", "## User", "question", "### Reasoning", "thought", "## Assistant", "answer", "### Tool: bash", "````", "done"} {
		if !strings.Contains(text, want) {
			t.Fatalf("export missing %q:\n%s", want, text)
		}
	}
}

func TestWriteStreamingExportDoesNotPublishPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.md")
	err := writeStreamingExport(path, func(dst io.Writer) error {
		_, _ = dst.Write([]byte("partial"))
		return errors.New("stop")
	})
	if err == nil {
		t.Fatal("expected write failure")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("partial destination published: %v", statErr)
	}
}
