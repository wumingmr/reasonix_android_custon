package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestEmptyPositionalRangeIsNoopOnlyBelowHardCeiling(t *testing.T) {
	for _, overLimit := range []bool{false, true} {
		sess := NewSession("system")
		content := "first question"
		if overLimit {
			content = strings.Repeat("question ", 40_000)
		}
		sess.Add(provider.Message{Role: provider.RoleUser, Content: content})
		a := New(nil, nil, sess, Options{ContextWindow: 32_000}, event.Discard)
		if overLimit && a.ContextMaintenanceSnapshot().ProjectedTokens < a.hardInputCeiling() {
			t.Fatal("fixture must exceed the measured hard ceiling")
		}
		before := a.currentProjectionVersion()
		err := a.SummarizeUpTo(t.Context(), 1)
		if (err != nil) != overLimit {
			t.Fatalf("overLimit=%v: empty range error=%v", overLimit, err)
		}
		if a.currentProjectionVersion() != before {
			t.Fatal("empty range installed a projection")
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := a.SummarizeUpTo(ctx, 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled empty range = %v", err)
		}
	}
}

func TestCancellationBeforeTruncationRescueDoesNotInstallProjection(t *testing.T) {
	for _, trigger := range []string{CompactionTriggerManual, CompactionTriggerPressure, CompactionTriggerOverflow} {
		t.Run(trigger, func(t *testing.T) {
			a := agentOverForce(t, &failingSummaryProvider{}, foldableSessionOverForce(14))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.svc.sink = event.FuncSink(func(e event.Event) {
				if e.Kind == event.ContextMaintenanceEvent && e.Maintenance != nil &&
					(e.Maintenance.Status == "failed" || e.Maintenance.Status == "blocked") {
					cancel()
				}
			})
			before := a.currentProjectionVersion()
			_, err := a.contextManager().Prepare(ctx, ContextPreparePolicy{Trigger: trigger, Force: trigger == CompactionTriggerManual})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Prepare error = %v, want context.Canceled", err)
			}
			if got := a.currentProjectionVersion(); got != before {
				t.Fatalf("projection version = %d, want unchanged %d", got, before)
			}
		})
	}
}

func TestEmptyManualCompactDoesNotCallSummarizer(t *testing.T) {
	provider := &failingSummaryProvider{}
	a := New(provider, nil, NewSession("sys"), Options{ContextWindow: 32_000}, event.Discard)
	if err := a.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("CompactNow(empty) = %v, want nil", err)
	}
	if provider.calls != 0 {
		t.Fatalf("summarizer calls = %d, want 0", provider.calls)
	}
	if a.currentProjectionVersion() != 0 || a.sess.compactionState.LastReceipt != nil {
		t.Fatal("empty compaction changed projection state")
	}
}

type lateSummaryProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *lateSummaryProvider) Name() string { return "late-summary" }
func (p *lateSummaryProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.once.Do(func() { close(p.started) })
	<-p.release // deliberately ignore cancellation like a broken adapter
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "# Goal\nKeep the task safe."}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

func TestCancelledLateSummaryCannotCommit(t *testing.T) {
	prov := &lateSummaryProvider{started: make(chan struct{}), release: make(chan struct{})}
	sess := NewSession("sys")
	for range 8 {
		sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("question ", 300)})
		sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("answer ", 300)})
	}
	a := New(prov, nil, sess, Options{ContextWindow: 32_000}, event.Discard)
	before := a.ContextMaintenanceSnapshot().ProjectionVersion
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.CompactNow(ctx, "") }()
	select {
	case <-prov.started:
	case <-time.After(10 * time.Second):
		t.Fatal("summary request did not start")
	}
	cancel()
	close(prov.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CompactNow = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("late summary did not return")
	}
	if after := a.ContextMaintenanceSnapshot().ProjectionVersion; after != before {
		t.Fatalf("cancelled late summary committed projection version %d -> %d", before, after)
	}
}
