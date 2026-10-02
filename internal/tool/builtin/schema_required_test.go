package builtin

import (
	"encoding/json"
	"testing"

	"reasonix/internal/tool"
)

func TestBuiltinContractSchemasDeclareRequiredArray(t *testing.T) {
	seen := false
	for _, entry := range tool.BuiltinContractEntries() {
		var root map[string]json.RawMessage
		if err := json.Unmarshal(entry.Schema, &root); err != nil {
			t.Fatalf("%s: schema is not an object: %v", entry.Name, err)
		}
		var required []string
		if raw, ok := root["required"]; !ok || string(raw) == "null" || json.Unmarshal(raw, &required) != nil {
			t.Errorf("%s: root schema lacks a required array: %s", entry.Name, entry.Schema)
		}
		if entry.Name == "get_goal" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("get_goal is not a registered builtin")
	}
}
