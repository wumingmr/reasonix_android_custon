// Package agentpreset keeps retired session-role values readable. Every
// recognized value now folds to standard; delivery no longer changes runtime
// behavior.
package agentpreset

import (
	"fmt"
	"strings"
)

// AgentPreset is the session role label.
type AgentPreset string

const (
	// Standard is the 默认 (standard) floor: the adaptive policy unchanged.
	Standard AgentPreset = "standard"
	// Delivery is retained only as a legacy input value.
	Delivery AgentPreset = "delivery"
)

// Normalize maps free-form and legacy values onto the canonical label. Light
// and its aliases fold to Standard; unknown values are an error so no new
// vocabulary can appear.
func Normalize(raw string) (AgentPreset, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(Standard), string(Delivery), "deliver", "quality", "balanced", "full", "normal",
		"light", "economy", "eco", "save", "saving", "low", "lite", "minimal":
		return Standard, nil
	default:
		return "", fmt.Errorf("unknown retired role setting %q", raw)
	}
}

// LegacyTokenMode returns the deprecated dual-write tokenMode value older
// clients expect next to a persisted preset. It is a wire-compat mapping only.
func LegacyTokenMode(p AgentPreset) string {
	return "full"
}

// FromLegacyTokenMode maps a persisted or CLI tokenMode onto a preset label.
func FromLegacyTokenMode(mode string) AgentPreset {
	p, err := Normalize(mode)
	if err != nil {
		return Standard
	}
	return p
}

// FloorNotice is printed once when a legacy mode value is folded.
const FloorNotice = "The preset setting has been retired. Recognized legacy values now use standard execution."

// String returns the canonical identifier.
func (p AgentPreset) String() string {
	return string(p)
}
