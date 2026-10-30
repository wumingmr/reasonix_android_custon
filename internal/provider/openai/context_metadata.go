package openai

import "encoding/json"

// modelCapacity is what one /models row declares about how much it can hold.
// Zero means the row said nothing: an absent field is not a claim that the
// model has no limit, so callers must treat zero as "unknown", never as "none".
type modelCapacity struct {
	// ContextWindow is the total prompt+completion budget the endpoint reports.
	ContextWindow int
	// MaxOutputTokens is the completion ceiling the endpoint reports, when it
	// reports one separately from the window.
	MaxOutputTokens int
}

// modelCapacityKeys are read in order; the first positive value wins. Official
// OpenAI-shaped listings use none of them, vLLM/SGLang use max_model_len,
// Together and Groq use context_length, and several aggregators use
// max_context_length or max_input_tokens.
var (
	modelContextWindowKeys   = []string{"context_length", "max_context_length", "max_model_len", "max_input_tokens"}
	modelMaxOutputTokenKeys  = []string{"max_completion_tokens", "max_output_tokens"}
	modelCapacityNestedScope = []string{"top_provider", "capabilities", "limits"}
)

// parseModelCapacity reads a listing row's declared capacity. Nested scopes are
// consulted after the top level, because a row that states its own numbers is
// more specific than the provider block summarising them.
func parseModelCapacity(entry map[string]json.RawMessage) modelCapacity {
	out := modelCapacity{
		ContextWindow:   firstPositiveInt(entry, modelContextWindowKeys),
		MaxOutputTokens: firstPositiveInt(entry, modelMaxOutputTokenKeys),
	}
	if out.ContextWindow > 0 && out.MaxOutputTokens > 0 {
		return out
	}
	for _, scope := range modelCapacityNestedScope {
		var nested map[string]json.RawMessage
		if json.Unmarshal(entry[scope], &nested) != nil {
			continue
		}
		if out.ContextWindow <= 0 {
			out.ContextWindow = firstPositiveInt(nested, modelContextWindowKeys)
		}
		if out.MaxOutputTokens <= 0 {
			out.MaxOutputTokens = firstPositiveInt(nested, modelMaxOutputTokenKeys)
		}
		if out.ContextWindow > 0 && out.MaxOutputTokens > 0 {
			return out
		}
	}
	return out
}

// firstPositiveInt returns the first key whose value decodes as a positive
// integer. A malformed or non-positive value is skipped rather than clamped:
// "0" and "-1" are how some listings spell "not applicable", and adopting them
// would either disable compaction or claim a ceiling nobody stated.
func firstPositiveInt(entry map[string]json.RawMessage, keys []string) int {
	for _, key := range keys {
		raw, present := entry[key]
		if !present {
			continue
		}
		var value int
		if json.Unmarshal(raw, &value) == nil && value > 0 {
			return value
		}
	}
	return 0
}

// mergeModelCapacity folds a duplicate row for the same model id into what is
// already known. The smaller positive window wins: overstating a window makes
// the provider reject a request, while understating it only compacts earlier,
// and a listing that repeats an id is usually two routes onto the same model
// with different headroom.
func mergeModelCapacity(previous, next modelCapacity) modelCapacity {
	out := previous
	if next.ContextWindow > 0 && (out.ContextWindow <= 0 || next.ContextWindow < out.ContextWindow) {
		out.ContextWindow = next.ContextWindow
	}
	if next.MaxOutputTokens > 0 && (out.MaxOutputTokens <= 0 || next.MaxOutputTokens < out.MaxOutputTokens) {
		out.MaxOutputTokens = next.MaxOutputTokens
	}
	return out
}
