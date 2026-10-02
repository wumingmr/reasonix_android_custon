package agent

import (
	"context"
	"encoding/json"
	"testing"

	"reasonix/internal/evidence"
	"reasonix/internal/runtimepolicy"
)

// A closed-loop turn is a delivery-floor turn: the floor alone arms the
// readiness pause, so the scope on its own no longer produces one.
func withClosedLoopContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = runtimepolicy.WithContext(ctx, runtimepolicy.Constraints{})
	return WithDeliveryExecutionScope(ctx, DeliveryExecutionScope{ID: "test-closed-loop", TaskText: "closed-loop test"})
}

func withNoClosedLoop(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func TestGoalScopeDoesNotRequireRiskDrivenReview(t *testing.T) {
	for _, path := range []string{"internal/auth/session.go", "schema/migration.sql", "README.md"} {
		ledger := evidence.NewLedger()
		ledger.Record(evidence.ReceiptFromToolCall("edit_file", json.RawMessage(`{"path":"`+path+`"}`), true, false))
		a := &Agent{task: taskRuntime{ledger: ledger}, turn: turnRuntime{deliveryScopeActive: true}}
		if result := a.ReadinessResult(); !result.Ready || len(result.Missing) != 0 {
			t.Fatalf("%s acquired a review gate: %+v", path, result)
		}
		if len(ledger.Receipts()) != 1 || !ledger.Receipts()[0].Success {
			t.Fatal("completion query changed execution evidence")
		}
	}
}
