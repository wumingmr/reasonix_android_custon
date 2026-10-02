package main

import "encoding/json"

func migrationNestedUnknown(body []byte, known ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	for _, key := range known {
		delete(fields, key)
	}
	return fields, nil
}
func migrationNestedMerge(value any, extra map[string]json.RawMessage) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	for key, value := range extra {
		if _, ok := fields[key]; !ok {
			fields[key] = value
		}
	}
	return json.Marshal(fields)
}
func (v *desktopMigrationReceipt) UnmarshalJSON(body []byte) error {
	type plain desktopMigrationReceipt
	var out plain
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	extra, err := migrationNestedUnknown(body, "targetSessionId", "contentDigest", "sourceRevision")
	*v = desktopMigrationReceipt(out)
	v.extra = extra
	return err
}
func (v desktopMigrationReceipt) MarshalJSON() ([]byte, error) {
	type plain desktopMigrationReceipt
	return migrationNestedMerge(plain(v), v.extra)
}
func (v *desktopMigrationConversion) UnmarshalJSON(body []byte) error {
	type plain desktopMigrationConversion
	var out plain
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	extra, err := migrationNestedUnknown(body, "root", "sessionId", "headId", "legacyDir", "codec", "depth")
	*v = desktopMigrationConversion(out)
	v.extra = extra
	return err
}
func (v desktopMigrationConversion) MarshalJSON() ([]byte, error) {
	type plain desktopMigrationConversion
	return migrationNestedMerge(plain(v), v.extra)
}
