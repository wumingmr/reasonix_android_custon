package control

import (
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestTerminalFailureClassificationPreservesCancellation(t *testing.T) {
	for _, terminal := range []event.Event{
		{Kind: event.TurnDone, Cancelled: true},
		{Kind: event.TurnDone, Cancelled: true, Err: provider.ErrNoDeliverableOutput},
	} {
		got := withTerminalFailureDiagnostic(terminal)
		if got.Diagnostic == nil || got.Diagnostic.Kind != provider.FailureKindCancelled {
			t.Fatalf("cancellation relabelled as delivery failure: %+v", got.Diagnostic)
		}
	}
	existing := &provider.FailureDiagnostic{Kind: "transport_protocol", ProviderID: "fixture"}
	got := withTerminalFailureDiagnostic(event.Event{Kind: event.TurnDone, Diagnostic: existing})
	if got.Diagnostic != existing || got.Detail != "Connection ID: fixture" {
		t.Fatalf("existing classification lost: %+v", got)
	}
}
