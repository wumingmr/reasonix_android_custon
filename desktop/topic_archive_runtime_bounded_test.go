package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/identitylock"
	"reasonix/internal/session"
)

func TestArchiveRuntimeMutationOutlastsBriefBackgroundHolders(t *testing.T) {
	a := &App{}
	// A background owner that keeps re-taking the lock for short slices is
	// what a single TryLock loses to on every attempt.
	stop := make(chan struct{})
	held := make(chan struct{})
	go func() {
		first := true
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.runtimeRebuildMu.Lock()
			if first {
				close(held)
				first = false
				time.Sleep(80 * time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
			a.runtimeRebuildMu.Unlock()
			time.Sleep(30 * time.Millisecond)
		}
	}()
	defer close(stop)
	<-held
	if _, ok := a.tryLockRuntimeMutation("probe"); ok {
		t.Fatal("fixture did not hold the runtime mutation lock")
	}
	release, ok := a.tryLockRuntimeMutationBounded("archive session")
	if !ok {
		t.Fatal("user archive gave up while the holder only held brief slices")
	}
	release()
}

func TestArchiveRuntimeMutationStillRefusesAPersistentHolder(t *testing.T) {
	a := &App{}
	a.runtimeAdmissionMu.RLock()
	defer a.runtimeAdmissionMu.RUnlock()
	start := time.Now()
	if _, ok := a.tryLockRuntimeMutationBounded("archive session"); ok {
		t.Fatal("archive acquired the lock while a turn held admission")
	}
	if elapsed := time.Since(start); elapsed > runtimeMutationUserWait+time.Second {
		t.Fatalf("bounded wait overran its budget: %v", elapsed)
	}
	// The failed attempt must leave both locks free for the next owner.
	if !a.runtimeRebuildMu.TryLock() {
		t.Fatal("refused archive leaked runtimeRebuildMu")
	}
	a.runtimeRebuildMu.Unlock()
}

func TestSessionBusyCauseKeepsTheHolderIdentity(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errTopicArchiveBusy, "runtime_mutation"},
		{errTopicHasActiveWork, "active_work"},
		{fmt.Errorf("start: %w", agent.ErrSessionLeaseHeld), "session_lease"},
		{fmt.Errorf("maintenance: %w", session.ErrWriterOwned), "writer_owned"},
		{fmt.Errorf("source: %w", identitylock.ErrHeld), "source_lock"},
		{errHistoricalSourceBusy, "historical_source"},
	} {
		if got := sessionBusyCause(tc.err); got != tc.want {
			t.Errorf("sessionBusyCause(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// holdRuntimeMutationBriefly keeps runtimeRebuildMu for 100ms, longer than a
// single TryLock tolerates and well inside the bounded user wait.
func holdRuntimeMutationBriefly(t *testing.T, a *App) {
	t.Helper()
	a.runtimeRebuildMu.Lock()
	timer := time.AfterFunc(100*time.Millisecond, a.runtimeRebuildMu.Unlock)
	t.Cleanup(func() { timer.Stop() })
}

func TestTopicRemovalEntryPointsOutlastBriefRuntimeHolders(t *testing.T) {
	t.Run("trash topic", func(t *testing.T) {
		a := &App{}
		a.sessionRemovalMu.Lock()
		defer a.sessionRemovalMu.Unlock()
		holdRuntimeMutationBriefly(t, a)
		trace := topicArchiveTrace{}
		if _, _, err := a.commitTopicArchive("topic", &trace); trace.phase != "removal_lock" {
			t.Fatalf("trash gave up at the runtime lock: phase=%q err=%v", trace.phase, err)
		}
	})
	t.Run("archive topic", func(t *testing.T) {
		a := &App{}
		holdRuntimeMutationBriefly(t, a)
		if err := a.archiveCompatibleTopic(""); errors.Is(err, errTopicArchiveBusy) {
			t.Fatal("archive gave up at the runtime lock")
		}
	})
	t.Run("remove topic", func(t *testing.T) {
		a := NewApp()
		a.ctx = t.Context()
		installNoopRuntimeEvents(a)
		defer a.closeSessionServices()
		holdRuntimeMutationBriefly(t, a)
		out, _ := a.RemoveTopic(TopicRemovalRequest{OperationID: "op", ExpectedToken: "token", Target: TopicRemovalTarget{WorkspaceID: "ws", TopicID: "topic"}})
		if out.ErrorCode == "busy" {
			t.Fatal("remove topic gave up at the runtime lock")
		}
	})
}
