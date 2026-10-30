package openai

import (
	"encoding/json"
	"testing"
)

// A listing that states capacity is the only free source of truth about it.
// The window is read from whichever spelling the endpoint chose, because
// OpenAI-compatible gateways disagree on all of them.
func TestParseModelCapacityReadsTheDeclaredWindow(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  string
		want modelCapacity
	}{
		{"openrouter", `{"id":"m","context_length":1048576,"top_provider":{"max_completion_tokens":131072}}`,
			modelCapacity{ContextWindow: 1048576, MaxOutputTokens: 131072}},
		{"vllm", `{"id":"m","max_model_len":32768}`, modelCapacity{ContextWindow: 32768}},
		{"nested only", `{"id":"m","top_provider":{"context_length":65536}}`, modelCapacity{ContextWindow: 65536}},
		{"top level wins", `{"id":"m","context_length":8192,"top_provider":{"context_length":65536}}`,
			modelCapacity{ContextWindow: 8192}},
		{"output only", `{"id":"m","max_output_tokens":4096}`, modelCapacity{MaxOutputTokens: 4096}},
		{"silent", `{"id":"m"}`, modelCapacity{}},
		// "0" and "-1" are how some listings spell "not applicable". Adopting
		// either would disable compaction or claim a ceiling nobody stated.
		{"zero", `{"id":"m","context_length":0}`, modelCapacity{}},
		{"negative", `{"id":"m","context_length":-1}`, modelCapacity{}},
		{"wrong type", `{"id":"m","context_length":"1048576"}`, modelCapacity{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entry map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.row), &entry); err != nil {
				t.Fatal(err)
			}
			if got := parseModelCapacity(entry); got != tc.want {
				t.Fatalf("capacity = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A window belongs to the model, so the metadata and the capacity travel
// independently: either can be present without the other.
func TestParseModelTupleKeepsMetadataAndCapacitySeparate(t *testing.T) {
	info, capacity := parseModelTuple("https://relay.example.com/v1",
		[]byte(`{"id":"m","context_length":1000,"input_modalities":["text"]}`))
	if info.ID != "m" || len(info.InputModalities) != 1 {
		t.Fatalf("info = %+v", info)
	}
	if capacity.ContextWindow != 1000 {
		t.Fatalf("capacity = %+v", capacity)
	}

	info, capacity = parseModelTuple("https://relay.example.com/v1", []byte(`{"id":"m"}`))
	if info.ID != "m" || info.InputModalities != nil {
		t.Fatalf("info = %+v", info)
	}
	if capacity != (modelCapacity{}) {
		t.Fatalf("capacity = %+v, want none", capacity)
	}
}

// A listing that repeats an id is usually two routes onto the same model with
// different headroom. Overstating the window makes the provider refuse a
// request, understating it only compacts earlier, so the smaller one wins.
func TestMergeModelCapacityPrefersTheSmallerBound(t *testing.T) {
	merged := mergeModelCapacity(
		modelCapacity{ContextWindow: 200_000, MaxOutputTokens: 8_000},
		modelCapacity{ContextWindow: 128_000, MaxOutputTokens: 16_000},
	)
	if merged.ContextWindow != 128_000 || merged.MaxOutputTokens != 8_000 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged := mergeModelCapacity(modelCapacity{}, modelCapacity{}); merged.ContextWindow != 0 || merged.MaxOutputTokens != 0 {
		t.Fatalf("merging two silent rows invented %+v", merged)
	}
}
