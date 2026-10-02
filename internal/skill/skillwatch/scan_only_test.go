package skillwatch

import (
	"io"
	"testing"
)

// ScanOnly exists so a caller can hold a real, closable service without a child
// process. It must behave like the degraded path the service already has: the
// subscription lives, no physical watch exists, and the root falls back to
// backoff scanning.
func TestScanOnlyServesSubscriptionsWithoutAPhysicalWatch(t *testing.T) {
	svc := NewService(Options{ScanOnly: true, Stderr: io.Discard})
	defer svc.Close()
	sub := svc.Subscribe(t.TempDir(), 2, countingScope, flatHash, func(string) {})
	defer sub.Release()

	diag := svc.Diagnostics()
	if diag.LogicalSubscriptions != 1 {
		t.Fatalf("logical subscriptions = %d, want 1", diag.LogicalSubscriptions)
	}
	if diag.PhysicalWatches != 0 {
		t.Fatalf("physical watches = %d, want 0: scan-only must not register any", diag.PhysicalWatches)
	}
	if diag.DegradedRoots != 1 {
		t.Fatalf("degraded roots = %d, want 1: the root must fall back to scanning", diag.DegradedRoots)
	}
}
