package transcript

import (
	"context"
	"errors"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/provider"
	"reasonix/internal/turnevent"
)

func TestSnapshotRetainsTerminalRecoveryAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    event.Event
		code string
	}{
		{"protocol", event.Event{Status: event.TurnFailed, Err: errors.New("provider failed"), ProtocolRecovery: &provider.ProtocolRecoveryAction{ID: "recover"}}, "protocol_recovery"},
		{"readiness", event.Event{Status: event.TurnFailed, Outcome: event.TurnOutcomeFinalReadiness, Readiness: &event.FinalReadiness{Missing: []string{"checks"}}}, event.NoticeCodeFinalReadiness},
		{"read pause", event.Event{Status: event.TurnFailed, Outcome: event.TurnOutcomeIncompleteRead, ReadPause: &provider.ReadPause{}}, event.TurnOutcomeIncompleteRead},
		{"read completion", event.Event{Status: event.TurnCompleted, ReadCompletion: &provider.ReadCompletion{ID: "run"}}, "read_completion"},
		{"cancel", event.Event{Status: event.TurnInterrupted}, event.NoticeCodeCancelledTurn},
		{"unknown effect", event.Event{Status: event.TurnRecoveryRequired}, event.NoticeCodeCancelledTurn},
		{"failure", event.Event{Status: event.TurnFailed, Err: errors.New("provider failed")}, event.NoticeCodeProviderRequestFailed},
		{"recovery pause", event.Event{Status: event.TurnFailed, Outcome: event.TurnOutcomeRecoveryPaused}, event.TurnOutcomeRecoveryPaused},
		{"uncertain", event.Event{Status: event.TurnCompleted, Outcome: event.TurnOutcomeCompletionUncertain}, event.TurnOutcomeCompletionUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewProjection(testIdentity, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			tc.e.Kind = event.TurnDone
			w := eventwire.ToWire(tc.e)
			if err := p.Apply(turnevent.Envelope{SessionID: testIdentity.SessionID, RuntimeEpoch: testIdentity.RuntimeEpoch, TurnID: "turn", Sequence: 1, Kind: w.Kind, Status: tc.e.Status, Event: w}); err != nil {
				t.Fatal(err)
			}
			check := func(p *Projection) {
				t.Helper()
				for _, row := range snapshot(t, p).Records {
					if row.Message.Code != tc.code {
						continue
					}
					if tc.e.ProtocolRecovery != nil && (row.Message.ProtocolRecovery == nil || row.Message.ProtocolRecovery.ID != "recover" || !row.Message.Pending) {
						t.Fatal("lost recovery token")
					}
					if tc.e.Readiness != nil && (row.Message.Readiness == nil || len(row.Message.Readiness.Missing) != 1) {
						t.Fatal("lost readiness")
					}
					if tc.e.ReadPause != nil && row.Message.ReadPause == nil {
						t.Fatal("lost read pause")
					}
					return
				}
				t.Fatalf("covered terminal event but lost %s", tc.code)
			}
			check(p)
			state, err := p.Checkpoint("digest")
			if err != nil {
				t.Fatal(err)
			}
			restored, err := RestoreCheckpoint(state, testIdentity)
			if err != nil {
				t.Fatal(err)
			}
			check(restored)
		})
	}
}

func TestInterruptedTurnKeepsProviderFailure(t *testing.T) {
	quota := &provider.QuotaError{Status: 402, Provider: "relay", Protocol: "openai"}
	for _, tc := range []struct {
		name     string
		e        event.Event
		wantFail bool
	}{
		{"recovery required with quota error", event.Event{Status: event.TurnRecoveryRequired, Err: quota, Diagnostic: provider.DiagnoseFailure(quota),
			Recovery: &event.RecoveryStatus{State: "recovery_required", Reason: "silent_interruption"}}, true},
		{"interrupted with quota error", event.Event{Status: event.TurnInterrupted, Err: quota, Diagnostic: provider.DiagnoseFailure(quota)}, true},
		{"user cancel", event.Event{Status: event.TurnInterrupted, Err: context.Canceled, Diagnostic: provider.DiagnoseFailure(context.Canceled)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewProjection(testIdentity, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			tc.e.Kind = event.TurnDone
			w := eventwire.ToWire(tc.e)
			if err := p.Apply(turnevent.Envelope{SessionID: testIdentity.SessionID, RuntimeEpoch: testIdentity.RuntimeEpoch, TurnID: "turn", Sequence: 1, Kind: w.Kind, Status: tc.e.Status, Event: w}); err != nil {
				t.Fatal(err)
			}
			var interrupted bool
			var failure *Message
			for _, row := range snapshot(t, p).Records {
				switch row.Message.Code {
				case event.NoticeCodeCancelledTurn:
					interrupted = true
				case event.NoticeCodeProviderRequestFailed:
					failure = &row.Message
				}
			}
			if !interrupted {
				t.Fatal("interrupted notice missing")
			}
			if !tc.wantFail {
				if failure != nil {
					t.Fatalf("cancelled turn rendered a provider failure: %q", failure.Content)
				}
				return
			}
			if failure == nil || !strings.Contains(failure.Content, "HTTP 402") || failure.Level != "warn" || failure.Diagnostic == nil || failure.Diagnostic.Status != 402 {
				t.Fatalf("provider failure hidden behind interrupted notice: %+v", failure)
			}
		})
	}
}

func TestHistoryRetainsReadReceipts(t *testing.T) {
	rows := History([]provider.Message{
		{ID: "pause", LocalOnly: true, ReadPause: &provider.ReadPause{}},
		{ID: "complete", LocalOnly: true, ReadCompletion: &provider.ReadCompletion{ID: "run"}},
	}, HistoryOptions{})
	if len(rows) != 2 || rows[0].ReadPause == nil || rows[1].ReadCompletion == nil {
		t.Fatalf("lost read receipts: %+v", rows)
	}
}

func TestCheckpointOwnsNestedRecoveryMetadata(t *testing.T) {
	p, err := NewProjection(testIdentity, []Message{{RecordID: "receipt", Role: "notice", ReadCompletion: &provider.ReadCompletion{
		ID: "run", Reads: []provider.CompletedRead{{Covered: [][2]int{{0, 12}}}},
	}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := p.Checkpoint("digest")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Records[0].ReadCompletion.Reads[0].Covered[0][1] = 99
	if got := snapshot(t, p).Records[0].Message.ReadCompletion.Reads[0].Covered[0][1]; got != 12 {
		t.Fatalf("checkpoint mutated live projection: %d", got)
	}
}
