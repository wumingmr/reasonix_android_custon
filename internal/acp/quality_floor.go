package acp

import "reasonix/internal/control"

// withoutQualityFloorConfig normalizes the retired execution-profile surface
// even when an older config provider still publishes it.
func withoutQualityFloorConfig(state SessionConfigState) SessionConfigState {
	state.RuntimeProfile = control.QualityFloorStandard
	options := make([]SessionConfigOption, 0, len(state.ConfigOptions))
	for _, option := range state.ConfigOptions {
		switch normalizeConfigID(option.ID) {
		case "quality_floor", "work_mode", "agent_preset":
			continue
		default:
			options = append(options, option)
		}
	}
	state.ConfigOptions = options
	return state
}
