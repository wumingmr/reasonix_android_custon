package taskcontract

import (
	"reasonix/internal/evidence"
	"testing"
)

func TestRetiredPolicyDoesNotRewriteHistoricalReceipt(t *testing.T) {
	for _, stamp := range []string{"", "standard", "delivery"} {
		receipt := evidence.Receipt{PolicyFloor: stamp, ToolName: "bash", Command: "go test ./...", Success: false}
		if ReceiptPolicyFloor(receipt) != PolicyFloorNone {
			t.Fatalf("stamp %q revived policy", stamp)
		}
		if receipt.PolicyFloor != stamp || receipt.Success {
			t.Fatal("compatibility read rewrote history")
		}
	}
}
