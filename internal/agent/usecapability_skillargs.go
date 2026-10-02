package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

func normalizeSkillCapabilityArgs(skillName string, args json.RawMessage) json.RawMessage {
	raw := strings.TrimSpace(string(args))
	if raw == "" || raw == "null" {
		return marshalSkillPayload(map[string]any{"name": skillName})
	}
	var text string
	if json.Unmarshal(args, &text) == nil {
		return normalizeSkillCapabilityText(skillName, text)
	}
	var payload map[string]any
	if json.Unmarshal(args, &payload) == nil {
		return normalizeSkillCapabilityObject(skillName, payload)
	}
	return args
}

func normalizeSkillCapabilityText(skillName, text string) json.RawMessage {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return marshalSkillPayload(map[string]any{"name": skillName})
	}
	if strings.HasPrefix(trimmed, "{") {
		var payload map[string]any
		if json.Unmarshal([]byte(trimmed), &payload) == nil {
			return normalizeSkillCapabilityObject(skillName, payload)
		}
	}
	return marshalSkillPayload(map[string]any{"name": skillName, "arguments": trimmed})
}

func normalizeSkillCapabilityObject(skillName string, payload map[string]any) json.RawMessage {
	if _, ok := payload["name"]; !ok {
		payload["name"] = skillName
	}
	if _, ok := payload["arguments"]; !ok {
		if task, hasTask := payload["task"]; hasTask {
			payload["arguments"] = skillCapabilityTaskArgument(task)
		}
	}
	return marshalSkillPayload(payload)
}

func skillCapabilityTaskArgument(task any) string {
	if text, ok := task.(string); ok {
		return strings.TrimSpace(text)
	}
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Sprint(task)
	}
	return string(data)
}

func marshalSkillPayload(payload map[string]any) json.RawMessage {
	data, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
