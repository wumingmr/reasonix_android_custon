package control

import (
	"fmt"
	"strings"

	"reasonix/internal/taskcontract"
)

func errUnknownQualityFloor(raw string) error {
	return fmt.Errorf("unknown retired quality floor %q", raw)
}

// Legacy quality-floor vocabulary. Delivery remains accepted at compatibility
// boundaries but always folds to standard.

const (
	QualityFloorStandard = "standard"
	QualityFloorDelivery = "delivery"
)

// NormalizeQualityFloor maps every recognized legacy label onto standard.
// Unknown values remain errors so no new vocabulary can appear.
func NormalizeQualityFloor(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", QualityFloorStandard, QualityFloorDelivery, "deliver", "quality", "balanced", "full", "normal":
		return QualityFloorStandard, nil
	case "light", "economy", "eco", "lite", "save", "saving", "low", "minimal":
		return QualityFloorStandard, nil
	default:
		return "", errUnknownQualityFloor(raw)
	}
}

func QualityFloorConstraint(string) taskcontract.PolicyFloor {
	return taskcontract.PolicyFloorNone
}

// QualityFloor returns the session floor's wire label.
func (c *Controller) QualityFloor() string {
	return QualityFloorStandard
}

// SetQualityFloor validates a retired compatibility value. It never mutates
// session state or the provider-visible prefix.
func (c *Controller) SetQualityFloor(floor string) error {
	_, err := NormalizeQualityFloor(floor)
	if err != nil {
		return err
	}
	return nil
}

func (c *Controller) qualityFloorConstraint() taskcontract.PolicyFloor {
	return taskcontract.PolicyFloorNone
}

// sessionSettings holds the remaining per-session planning posture.
type sessionSettings struct {
	planMode bool
}
