package historywork

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReaderBoundsDecoderReadsAndChecksCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	r := &Reader{Context: ctx, Source: strings.NewReader(strings.Repeat("x", 2*ReadChunk))}
	buf := make([]byte, 2*ReadChunk)
	if n, err := r.Read(buf); err != nil || n != ReadChunk {
		t.Fatalf("read=%d %v", n, err)
	}
	cancel()
	if n, err := r.Read(buf); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read=%d %v", n, err)
	}
}

func TestForegroundPausesBackgroundAndReleaseIsIdempotent(t *testing.T) {
	var c Coordinator
	release, err := c.Foreground(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Background(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("background admission=%v", err)
	}
	release()
	release()
	bg, err := c.Background(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	bg()
	bg()
}

func TestYieldingBackgroundDoesNotTrapPriorityMetadataBehindForeground(t *testing.T) {
	var c Coordinator
	foreground, err := c.Foreground(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer foreground()
	if _, err := c.BackgroundSliceYielding(t.Context(), false); !errors.Is(err, ErrForegroundActive) {
		t.Fatalf("P2 must yield to its queue, got %v", err)
	}
	metadata, err := c.BackgroundSliceYielding(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	metadata(0)
	if got := c.Diagnostics(); got.BackgroundSlices != 1 || got.ForegroundActive != 1 {
		t.Fatalf("metadata incorrectly waited for or released execution preparation: %+v", got)
	}
}
