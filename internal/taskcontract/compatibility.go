package taskcontract

import "reasonix/internal/evidence"

// PolicyFloor preserves the retired public type. Historical receipt fields
// remain readable, but never rebuild execution or acceptance obligations.
type PolicyFloor uint8

const (
	PolicyFloorNone PolicyFloor = iota
	PolicyFloorDelivery
)

// ParsePolicyFloor keeps historical receipt values readable. The delivery
// policy is retired, so every value now has the standard effect.
func ParsePolicyFloor(string) PolicyFloor {
	return PolicyFloorNone
}

func (f PolicyFloor) String() string {
	if f == PolicyFloorDelivery {
		return "delivery"
	}
	return "standard"
}

// ReceiptPolicyFloor reads the floor stamp from a receipt. Unstamped
// historical receipts replay as standard.
func ReceiptPolicyFloor(rec evidence.Receipt) PolicyFloor {
	return ParsePolicyFloor(rec.PolicyFloor)
}
