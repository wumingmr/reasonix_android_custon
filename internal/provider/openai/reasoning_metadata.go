package openai

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"reasonix/internal/provider"
)

var reasoningLevelID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Only declarations are decoded; a model name or parameter flag is not an enum.
func parseReasoningMetadata(entry map[string]json.RawMessage) *provider.ModelReasoningInfo {
	r := &provider.ModelReasoningInfo{}
	present := false
	readBool := func(raw json.RawMessage) {
		present = true
		var value *bool
		if json.Unmarshal(raw, &value) != nil || value == nil {
			r.Invalid = true
			return
		}
		if r.Supported != nil && *r.Supported != *value {
			r.Invalid = true
		}
		r.Supported = value
	}
	if raw, ok := entry["reasoning"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil && fields != nil {
			present = true
			if v, ok := fields["supported"]; ok {
				readBool(v)
			}
			entryCopy := make(map[string]json.RawMessage, len(entry)+3)
			for k, v := range entry {
				entryCopy[k] = v
			}
			for key, alias := range map[string]string{"protocol": "reasoning_protocol", "efforts": "supported_efforts", "default": "default_effort"} {
				if v, ok := fields[key]; ok {
					if previous, exists := entryCopy[alias]; exists && string(previous) != string(v) {
						r.Invalid = true
					}
					entryCopy[alias] = v
				}
			}
			entry = entryCopy
		} else {
			readBool(raw)
		}
	}
	if raw, ok := entry["supports_reasoning"]; ok {
		readBool(raw)
	}
	present = parseDeclaredProtocol(entry, r) || present
	present = parseDeclaredEfforts(entry, r) || present
	if raw, ok := entry["default_effort"]; ok {
		present = true
		if json.Unmarshal(raw, &r.Default) != nil || r.Default == "auto" {
			r.Invalid = true
		}
	}
	if r.Default != "" && len(r.Efforts) > 0 && !slices.Contains(r.Efforts, r.Default) {
		r.Invalid = true
	}
	if r.Supported != nil && !*r.Supported && len(r.Efforts) > 0 {
		r.Invalid = true
	}
	r.Parameter = hasReasoningParameter(entry["supported_parameters"])
	present = r.Parameter || present
	if !present {
		return nil
	}
	return r
}

func hasReasoningParameter(raw json.RawMessage) bool {
	var fields []string
	return json.Unmarshal(raw, &fields) == nil && (slices.Contains(fields, "reasoning") || slices.Contains(fields, "reasoning_effort") || slices.Contains(fields, "thinking"))
}

func parseDeclaredProtocol(entry map[string]json.RawMessage, r *provider.ModelReasoningInfo) bool {
	raw, ok := entry["reasoning_protocol"]
	if !ok {
		return false
	}
	if json.Unmarshal(raw, &r.Protocol) != nil {
		r.Invalid = true
	}
	switch r.Protocol {
	case "deepseek", "glm", "kimi-k3", "openai", "none":
	default:
		r.Invalid = true
	}
	return true
}

func parseDeclaredEfforts(entry map[string]json.RawMessage, r *provider.ModelReasoningInfo) bool {
	present := false
	for _, key := range []string{"supported_efforts", "reasoning_efforts"} {
		raw, ok := entry[key]
		if !ok {
			continue
		}
		present = true
		var ids []string
		if json.Unmarshal(raw, &ids) != nil || len(ids) == 0 || len(ids) > 32 {
			r.Invalid = true
			continue
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !reasoningLevelID.MatchString(id) || id == "auto" || seen[id] {
				r.Invalid = true
			}
			seen[id] = true
		}
		if r.Efforts != nil && !slices.Equal(r.Efforts, ids) {
			r.Invalid = true
		}
		r.Efforts = ids
	}
	return present
}

func mergeReasoningMetadata(a, b *provider.ModelReasoningInfo) *provider.ModelReasoningInfo {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if reflect.DeepEqual(a, b) {
		return a
	}
	// Conflicting duplicate model declarations never resurrect previous levels.
	return &provider.ModelReasoningInfo{Invalid: true, Parameter: a.Parameter || b.Parameter}
}

// Nested capabilities are declarations too; top-level fields have precedence.
func reasoningMetadataForModel(entry map[string]json.RawMessage) *provider.ModelReasoningInfo {
	copy := make(map[string]json.RawMessage, len(entry))
	for k, v := range entry {
		copy[k] = v
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(entry["capabilities"], &nested) == nil {
		for k, v := range nested {
			if _, exists := copy[k]; !exists && (strings.Contains(k, "reasoning") || k == "supported_efforts" || k == "default_effort") {
				copy[k] = v
			}
		}
	}
	return parseReasoningMetadata(copy)
}
