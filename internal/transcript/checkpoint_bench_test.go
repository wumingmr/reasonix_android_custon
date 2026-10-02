package transcript

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkTranscriptCheckpointLargeHistory(b *testing.B) {
	const count = 2048
	body := strings.Repeat("x", 16<<10)
	rows := make([]Message, count)
	for i := range rows {
		rows[i] = Message{MessageID: fmt.Sprint(i), Role: "assistant", Content: body}
	}
	p, err := NewProjection(testIdentity, rows, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(count * len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		state, err := p.Checkpoint("digest")
		if err != nil || len(state.Records) != count {
			b.Fatal("checkpoint failed", err)
		}
	}
}
