package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func newTestMachine(t *testing.T) *Machine {
	t.Helper()
	clock := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	return NewMachine(
		func() time.Time { return clock },
		func() string { return "goal-1" },
	)
}

func TestCreateDefaultsToUnlimitedAndArmed(t *testing.T) {
	m := newTestMachine(t)
	view, err := m.Create(CreateRequest{Objective: " ship the runtime "})
	if err != nil {
		t.Fatal(err)
	}
	if view.ID != "goal-1" || view.Revision != 1 || view.Objective != "ship the runtime" {
		t.Fatalf("view = %+v", view)
	}
	if view.MaxGoalRounds != nil {
		t.Fatalf("default limit = %v, want unlimited", *view.MaxGoalRounds)
	}
	if view.Activation != ActivationArmed || view.Phase != PhaseActive {
		t.Fatalf("phase/activation = %s/%s", view.Phase, view.Activation)
	}
}

func TestExplicitReplaceUsesNewIdentityAndPreservesClearTombstone(t *testing.T) {
	clock := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	next := 0
	m := NewMachine(func() time.Time { return clock }, func() string {
		next++
		return fmt.Sprintf("goal-%d", next)
	})
	first, err := m.Create(CreateRequest{Objective: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Replace(CreateRequest{Objective: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Revision != 1 || second.Objective != "second" {
		t.Fatalf("replacement = %+v, first = %+v", second, first)
	}
	encoded, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Cleared *Ref `json:"cleared"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if document.Cleared == nil || *document.Cleared != first.Ref() {
		t.Fatalf("clear tombstone = %+v, want %+v", document.Cleared, first.Ref())
	}
}

func TestLifecycleUsesExactRevision(t *testing.T) {
	m := newTestMachine(t)
	created, err := m.Create(CreateRequest{Objective: "ship"})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := m.Edit(created.Ref(), EditRequest{Objective: stringPtr("ship safely")})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != 2 || edited.Objective != "ship safely" {
		t.Fatalf("edited = %+v", edited)
	}
	_, err = m.Complete(created.Ref())
	var goalErr *Error
	if !errors.As(err, &goalErr) || goalErr.Code != ErrStaleRevision {
		t.Fatalf("stale complete error = %v", err)
	}
}

func TestRoundsDoNotChangeRevisionAndRespectExplicitLimit(t *testing.T) {
	m := newTestMachine(t)
	limit := uint64(2)
	created, err := m.Create(CreateRequest{Objective: "ship", MaxGoalRounds: &limit})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.AdmitRound(created.Ref())
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.AdmitRound(created.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != created.Revision || second.RoundsStarted != 2 || first.RoundsStarted != 1 {
		t.Fatalf("round views = %+v / %+v", first, second)
	}
	_, err = m.AdmitRound(created.Ref())
	if !errors.As(err, new(*Error)) || ErrorCodeOf(err) != ErrRoundLimit {
		t.Fatalf("third round error = %v", err)
	}
}

func TestColdRestoreIsDisarmedAndPreservesUnknownFields(t *testing.T) {
	m := newTestMachine(t)
	raw := []byte(`{"version":1,"current":{"id":"g","revision":4,"objective":"resume me","phase":"active","maxGoalRounds":null,"roundsStarted":8,"createdAt":"2026-09-13T10:00:00Z","updatedAt":"2026-09-13T10:00:00Z"},"futurePolicy":{"mode":"adaptive"}}`)
	view, err := m.Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || view.Activation != ActivationDisarmed || view.RoundsStarted != 8 {
		t.Fatalf("restored = %+v", view)
	}
	encoded, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["futurePolicy"]) != `{"mode":"adaptive"}` {
		t.Fatalf("futurePolicy = %s", got["futurePolicy"])
	}
}

func TestPausedGoalNeedsExplicitUserResume(t *testing.T) {
	m := newTestMachine(t)
	created, _ := m.Create(CreateRequest{Objective: "ship"})
	paused, err := m.Pause(created.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if paused.Phase != PhasePaused || paused.Activation != ActivationDisarmed {
		t.Fatalf("paused = %+v", paused)
	}
	if _, err := m.Resume(paused.Ref(), false); ErrorCodeOf(err) != ErrUserAuthorityRequired {
		t.Fatalf("model resume error = %v", err)
	}
	resumed, err := m.Resume(paused.Ref(), true)
	if err != nil || resumed.Phase != PhaseActive || resumed.Activation != ActivationArmed {
		t.Fatalf("resumed = %+v, err = %v", resumed, err)
	}
}

func TestBlockedRequiresReasonAndMinimumAutomaticRounds(t *testing.T) {
	m := newTestMachine(t)
	created, _ := m.Create(CreateRequest{Objective: "ship"})
	if _, err := m.Block(created.Ref(), BlockReason{}, false, 3); ErrorCodeOf(err) != ErrInvalidBlockReason {
		t.Fatalf("empty reason error = %v", err)
	}
	if _, err := m.Block(created.Ref(), BlockReason{Code: "dependency", Message: "waiting"}, false, 3); ErrorCodeOf(err) != ErrBlockedTooEarly {
		t.Fatalf("early block error = %v", err)
	}
	for range 3 {
		if _, err := m.AdmitRound(created.Ref()); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := m.Block(created.Ref(), BlockReason{Code: "dependency", Message: "waiting"}, false, 3)
	if err != nil || blocked.Phase != PhaseBlocked || blocked.Activation != ActivationDisarmed {
		t.Fatalf("blocked = %+v, err = %v", blocked, err)
	}
}

func TestCompleteMayHappenInFirstRound(t *testing.T) {
	m := newTestMachine(t)
	created, _ := m.Create(CreateRequest{Objective: "small but durable"})
	completed, err := m.Complete(created.Ref())
	if err != nil || completed.Phase != PhaseComplete || completed.Activation != ActivationDisarmed {
		t.Fatalf("completed = %+v, err = %v", completed, err)
	}
}

func TestDisarmDoesNotOverwriteTerminalStopReason(t *testing.T) {
	m := newTestMachine(t)
	created, err := m.Create(CreateRequest{Objective: "finish"})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := m.Complete(created.Ref())
	if err != nil {
		t.Fatal(err)
	}
	view := m.Disarm("cancelled")
	if view == nil || view.Phase != PhaseComplete || view.StopReason != completed.StopReason {
		t.Fatalf("terminal disarm = %+v, want stop reason %q", view, completed.StopReason)
	}
}

func TestUnlimitedGoalAdmitsPastHarnessDefaultCeiling(t *testing.T) {
	m := newTestMachine(t)
	created, err := m.Create(CreateRequest{Objective: "long-running target"})
	if err != nil {
		t.Fatal(err)
	}
	var view View
	for range 300 {
		view, err = m.AdmitRound(created.Ref())
		if err != nil {
			t.Fatal(err)
		}
	}
	if view.RoundsStarted != 300 || view.MaxGoalRounds != nil || view.Revision != created.Revision {
		t.Fatalf("unlimited goal view = %+v", view)
	}
}

func TestUnknownVersionFailsClosed(t *testing.T) {
	m := newTestMachine(t)
	_, err := m.Restore([]byte(`{"version":2,"current":null}`))
	if ErrorCodeOf(err) != ErrUnsupportedVersion {
		t.Fatalf("restore error = %v", err)
	}
}

func TestClonePreservesLiveActivationAndIsIndependent(t *testing.T) {
	m := newTestMachine(t)
	created, err := m.Create(CreateRequest{Objective: "ship"})
	if err != nil {
		t.Fatal(err)
	}
	clone := m.Clone()
	if clone.Get().Activation != ActivationArmed {
		t.Fatalf("clone activation = %s", clone.Get().Activation)
	}
	if _, err := clone.Complete(created.Ref()); err != nil {
		t.Fatal(err)
	}
	if m.Get().Phase != PhaseActive || clone.Get().Phase != PhaseComplete {
		t.Fatalf("original/clone = %s/%s", m.Get().Phase, clone.Get().Phase)
	}
}

func stringPtr(value string) *string { return &value }
