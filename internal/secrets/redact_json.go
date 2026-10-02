package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
)

// RedactJSON scrubs diagnostic text without applying text replacements to JSON
// syntax or numeric counters. UseNumber preserves sequence numbers exactly.
func RedactJSON(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, errors.New("invalid diagnostic JSON")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(redactJSONValue(value))
}

func redactJSONValue(value any) any {
	switch v := value.(type) {
	case string:
		return Redact(v)
	case []any:
		for i := range v {
			v[i] = redactJSONValue(v[i])
		}
	case map[string]any:
		for key, child := range v {
			if text, ok := child.(string); ok && credentialTextKeySensitive(key) && text != "" {
				v[key] = redactedValue
			} else {
				v[key] = redactJSONValue(child)
			}
		}
	}
	return value
}
