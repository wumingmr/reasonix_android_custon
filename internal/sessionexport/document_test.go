package sessionexport

import (
	"bytes"
	"strings"
	"testing"
)

func TestMarkdownPreservesToolBytesAndLongFences(t *testing.T) {
	var dst bytes.Buffer
	original := "  a\n\n\n``````\n中文  \n"
	if err := WriteItemMarkdown(&dst, Item{"kind": "tool", "name": "bash", "args": "{}", "output": original, "status": "done"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dst.String(), original) || !strings.Contains(dst.String(), "```````\n") {
		t.Fatalf("tool bytes or fence lost: %q", dst.String())
	}
}
