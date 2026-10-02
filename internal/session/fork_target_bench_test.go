package session

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// benchmarkSourceCommits is long enough that rebuilding the durable transcript
// and reading only the turn projection differ by orders of magnitude: it is the
// length of the history a long-lived session reaches in practice.
const benchmarkSourceCommits = 2000

// newBenchmarkSource builds the service and one live source with many committed
// turns. Every read of a live runtime is the hot path a remote surface refreshes
// after each turn, next to the running turn, so the fixture keeps the source
// live rather than closing it.
func newBenchmarkSource(b *testing.B, commits int) (*Service, SessionRef) {
	b.Helper()
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(b.TempDir(), "sessions-v4")))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(context.Background(), CreateOptions{SessionID: "source"})
	if err != nil {
		b.Fatal(err)
	}
	// Cleanup runs before the benchmark removes its temp directory, so an open
	// runtime stops flushing its durable log instead of racing that removal.
	b.Cleanup(func() { _ = service.Close(context.Background(), runtime.Ref()) })
	for index := range commits {
		turnID := fmt.Sprintf("turn-%d", index)
		payload, err := json.Marshal(map[string]any{"message": provider.Message{
			ID: "message-" + turnID, Role: provider.RoleAssistant, Content: strings.Repeat("x", 256),
		}})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := runtime.Session().AppendBatch(context.Background(), turnID, []Event{
			{Kind: "turn/start"},
			{Kind: "message/complete", Payload: payload},
			{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
		}); err != nil {
			b.Fatal(err)
		}
	}
	return service, runtime.Ref()
}

// BenchmarkForkTargetSetForLiveRuntime measures the fork-target read of a live
// source with a long history. The read serves the surface's per-turn refresh,
// so it must stay proportional to the turn projection and must not rebuild the
// durable transcript it does not read.
func BenchmarkForkTargetSetForLiveRuntime(b *testing.B) {
	service, ref := newBenchmarkSource(b, benchmarkSourceCommits)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		set, err := service.ForkTargetSetFor(context.Background(), ref)
		if err != nil {
			b.Fatal(err)
		}
		if len(set.Targets) != benchmarkSourceCommits {
			b.Fatalf("targets = %d, want %d", len(set.Targets), benchmarkSourceCommits)
		}
	}
}
