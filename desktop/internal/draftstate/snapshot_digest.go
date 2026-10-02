package draftstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SnapshotDigest hashes canonical JSON, preserving array order and exact text.
func SnapshotDigest(content, settings string) (string, error) {
	var body, profile any
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		return "", err
	}
	if err := json.Unmarshal([]byte(settings), &profile); err != nil {
		return "", err
	}
	if object, ok := body.(map[string]any); ok {
		if attachments, ok := object["attachments"].([]any); ok {
			for _, item := range attachments {
				if attachment, ok := item.(map[string]any); ok {
					delete(attachment, "previewUrl")
				}
			}
		}
	}
	if object, ok := profile.(map[string]any); ok && object["modelSource"] == "default" {
		// The concrete model is a compatibility mirror. Inherited defaults remain
		// live until submission, so they are not part of the editable draft's
		// semantic identity.
		delete(object, "model")
	}
	canonical, err := json.Marshal([]any{body, profile})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
