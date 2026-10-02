package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRunExercisesColdOpenAndHistoryIndex(t *testing.T) {
	result, err := run(t.Context(), config{
		Root: filepath.Join(t.TempDir(), "sessions-v4"), SessionID: "small-capacity",
		HistoryMessages: 6, HistoryBytes: 256 << 10,
		AttachmentBytes: 384 << 10, AttachmentChunkBytes: 128 << 10,
		WorksetBytes: 64 << 10, FlushEvery: 2, PageSamples: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.HistoryMessages != 6 || result.HistoryLogicalBytes != 256<<10 {
		t.Fatalf("history result = %+v", result)
	}
	if result.AttachmentObjects != 3 || result.AttachmentLogicalBytes != 384<<10 {
		t.Fatalf("attachment result = %+v", result)
	}
	if result.EventSequence != 13 || result.DurableSequence != result.EventSequence {
		t.Fatalf("watermarks = accepted %d durable %d", result.EventSequence, result.DurableSequence)
	}
	if result.ModelWorksetBytes != 64<<10 || result.SessionDiskBytes == 0 || result.ContentDiskBytes == 0 || result.QueryCacheDiskBytes == 0 {
		t.Fatalf("capacity evidence incomplete: %+v", result)
	}
}

func TestPercentile95(t *testing.T) {
	values := []time.Duration{5, 1, 3, 2, 4}
	if got := percentile95(values); got != 5 {
		t.Fatalf("p95 = %v, want 5", got)
	}
}
