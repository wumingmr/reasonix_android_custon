package openai

import (
	"encoding/json"
	"testing"
)

func TestReasoningMetadataOnlyTrustsDeclarations(t *testing.T) {
	cases := []struct {
		body             string
		present, invalid bool
		levels           int
	}{
		{`{"id":"deepseek-v4-pro"}`, false, false, 0},
		{`{"reasoning":true}`, true, false, 0},
		{`{"supported_parameters":["reasoning_effort"]}`, true, false, 0},
		{`{"reasoning_protocol":"openai","supported_efforts":["low","high"],"default_effort":"low"}`, true, false, 2},
		{`{"reasoning":{"protocol":"glm","efforts":["enabled","disabled"],"default":"enabled"}}`, true, false, 2},
		{`{"supported_efforts":["low","low"]}`, true, true, 2},
		{`{"supported_efforts":["auto"]}`, true, true, 1},
		{`{"supported_efforts":["low"],"default_effort":"high"}`, true, true, 1},
		{`{"reasoning":false,"supported_efforts":["high"]}`, true, true, 1},
		{`{"reasoning_protocol":"arbitrary"}`, true, true, 0},
	}
	for _, tc := range cases {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.body), &entry); err != nil {
			t.Fatal(err)
		}
		got := reasoningMetadataForModel(entry)
		if (got != nil) != tc.present {
			t.Fatal(tc.body)
		}
		if got != nil && (got.Invalid != tc.invalid || len(got.Efforts) != tc.levels) {
			t.Fatalf("%s: %+v", tc.body, got)
		}
	}
}

func TestReasoningMetadataDuplicateConflictStaysUnknown(t *testing.T) {
	var a, b map[string]json.RawMessage
	json.Unmarshal([]byte(`{"supported_efforts":["low"]}`), &a)
	json.Unmarshal([]byte(`{"supported_efforts":["high"]}`), &b)
	x := reasoningMetadataForModel(a)
	y := reasoningMetadataForModel(b)
	merged := mergeReasoningMetadata(x, y)
	if !merged.Invalid || !mergeReasoningMetadata(merged, x).Invalid {
		t.Fatal("conflicting declaration resurrected")
	}
}
