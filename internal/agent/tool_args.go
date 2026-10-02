package agent

import (
	"encoding/json"
	"strings"
)

func bashCommandFromArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return ""
	}
	var command string
	if err := json.Unmarshal(fields["command"], &command); err != nil {
		return ""
	}
	return strings.TrimSpace(command)
}

func boundedRecoveryTaskSummary(task string) string {
	task = strings.TrimSpace(task)
	const maxRunes = 800
	runes := []rune(task)
	if len(runes) <= maxRunes {
		return task
	}
	return string(runes[:maxRunes]) + "…"
}
