package agent

import (
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

const HistoricalChecksNoticeCode = "historical_checks"
const HistoricalChecksNoticeText = "Historical checks were not completed. The old quality policy is retired; these records do not block current work. Use /continue-checks to check the work explicitly."

// HistoricalChecks projects an old checkpoint without reviving its quality
// gate. Both history hosts and explicit recovery use this eligibility boundary.
func HistoricalChecks(recovery *provider.FinalReadinessRecovery) *event.FinalReadiness {
	if recovery == nil || !recovery.Pending {
		return nil
	}
	return &event.FinalReadiness{Attempts: 1, Missing: append([]string(nil), recovery.Missing...)}
}
