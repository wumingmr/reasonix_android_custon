package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

type slowSummaryProvider struct {
	output bool
	calls  int
}

type overflowThenSlowSummary struct {
	requests int
	summary  slowSummaryProvider
}

func (*overflowThenSlowSummary) Name() string { return "overflow-summary" }
func (p *overflowThenSlowSummary) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.requests++
	if p.requests == 1 {
		return nil, &provider.ContextLimitError{}
	}
	return p.summary.Stream(ctx, req)
}

func TestSamplingOverflowPreservesSummaryBudgetFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &overflowThenSlowSummary{}
		sess := foldableSessionOverForce(6)
		before := sess.Snapshot()
		a := New(p, nil, sess, Options{ContextWindow: 32000, TaskBudget: TaskBudget{Wall: 10 * time.Minute}}, event.Discard)
		started := time.Now()
		result := a.streamWithSamplingRecovery(t.Context(), 1)
		var summary *SummaryError
		if !errors.Is(result.err, ErrCompactionRequired) || !errors.As(result.err, &summary) || summary.Code != "summary_budget_exceeded" {
			t.Fatalf("sampling lost the compaction cause: %v", result.err)
		}
		if p.requests != 2 || time.Since(started) != compactionBudget {
			t.Fatalf("requests=%d elapsed=%s", p.requests, time.Since(started))
		}
		if !reflect.DeepEqual(before, sess.Snapshot()) || a.currentProjectionVersion() != 0 {
			t.Fatal("failed overflow recovery changed history or projection")
		}
	})
}

func (*slowSummaryProvider) Name() string { return "slow-summary" }
func (p *slowSummaryProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.calls++
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				chunk := provider.Chunk{Type: provider.ChunkUsage}
				if p.output {
					chunk = provider.Chunk{Type: provider.ChunkReasoning, Text: "working"}
				}
				select {
				case ch <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

func TestCompactionBudgetIncludesContinuousOutputAndSilentStreams(t *testing.T) {
	for _, output := range []bool{false, true} {
		for _, trigger := range []string{CompactionTriggerManual, CompactionTriggerPressure, CompactionTriggerOverflow} {
			t.Run(trigger+map[bool]string{false: "-silent", true: "-reasoning"}[output], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					p := &slowSummaryProvider{output: output}
					sess := foldableSessionOverForce(6)
					before := sess.Snapshot()
					a := New(p, nil, sess, Options{ContextWindow: 5000, CompactRatio: .5}, event.Discard)
					parent := context.Background()
					start := time.Now()
					_, err := a.contextManager().Prepare(parent, ContextPreparePolicy{Trigger: trigger})
					if trigger == CompactionTriggerPressure {
						if err != nil {
							t.Fatalf("safe pressure must continue: %v", err)
						}
					} else {
						var summary *SummaryError
						if !errors.As(err, &summary) || summary.Code != "summary_budget_exceeded" {
							t.Fatalf("error = %v", err)
						}
						if trigger == CompactionTriggerOverflow && !errors.Is(err, ErrCompactionRequired) {
							t.Fatal("budget failure lost the blocked-context classification")
						}
					}
					if time.Since(start) != compactionBudget || parent.Err() != nil || p.calls != 1 {
						t.Fatalf("elapsed=%s parent=%v calls=%d", time.Since(start), parent.Err(), p.calls)
					}
					if !reflect.DeepEqual(before, sess.Snapshot()) || a.currentProjectionVersion() != 0 {
						t.Fatal("timeout changed history")
					}
				})
			})
		}
	}
}

func TestCompactionBudgetCancelsQueuedOperationBeforeGateRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &slowSummaryProvider{}
		a := New(p, nil, foldableSessionOverForce(6), Options{ContextWindow: 5000}, event.Discard)
		a.sess.compactionRunMu.Lock()
		finished := make(chan error, 1)
		go func() { finished <- a.CompactNow(context.Background(), "") }()
		synctest.Wait()
		time.Sleep(compactionBudget)
		synctest.Wait()
		select {
		case err := <-finished:
			if !errors.Is(err, errSummaryBudget) {
				t.Fatal(err)
			}
		default:
			t.Fatal("expired waiter still needs the gate")
		}
		a.sess.compactionRunMu.Unlock()
		if p.calls != 0 {
			t.Fatal("expired waiter issued a request")
		}
	})
}

func TestNestedCompactionRunKeepsOriginalBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := New(nil, nil, NewSession("sys"), Options{}, event.Discard)
		ctx, finish := a.beginCompactionRun(context.Background())
		time.Sleep(4 * time.Minute)
		nested, end := a.beginCompactionRun(ctx)
		deadline, _ := nested.Deadline()
		if time.Until(deadline) != time.Minute || currentCompactionRun(ctx) != currentCompactionRun(nested) {
			t.Fatal("nested operation reset its budget")
		}
		_ = end(nil)
		_ = finish(nil)
	})
}
