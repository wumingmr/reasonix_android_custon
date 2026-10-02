package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

type timedExtractProvider struct {
	extractStubProvider
	started int
}

func (p *timedExtractProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.started++
	delay := 40 * time.Second
	if p.started == 1 {
		delay = 4 * time.Minute
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(delay):
		return p.extractStubProvider.Stream(ctx, req)
	}
}

func TestManualChunkedFallbackSharesInitialSummaryDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &timedExtractProvider{extractStubProvider: extractStubProvider{failFirst: 1, reply: "digest"}}
		sess := NewSession("sys")
		for range 12 {
			sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("u", 6000)})
			sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("a", 6000)})
		}
		original := sess.Snapshot()
		a := New(p, nil, sess, Options{ContextWindow: 100000, RecentKeep: 2}, event.Discard)
		started := time.Now()
		err := a.CompactNow(t.Context(), "")
		if !errors.Is(err, errSummaryBudget) || time.Since(started) != compactionBudget {
			t.Fatalf("elapsed=%s err=%v", time.Since(started), err)
		}
		if p.started != 3 {
			t.Fatalf("requests=%d, want initial + two fragment attempts", p.started)
		}
		if !reflect.DeepEqual(original, sess.Snapshot()) || a.currentProjectionVersion() != 0 {
			t.Fatal("incomplete chunk tree changed transcript or projection")
		}
	})
}
