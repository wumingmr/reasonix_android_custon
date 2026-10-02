package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// LegacySessionTopicID preserves the Desktop topic identity used before
// metadata-only discovery. Computing it never writes a branch sidecar.
func LegacySessionTopicID(path string) string {
	id := strings.TrimSpace(BranchID(path))
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	var b strings.Builder
	b.WriteString("legacy_")
	for _, r := range id {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	prefix := strings.TrimRight(b.String(), "_")
	if prefix == "legacy" {
		prefix = "legacy_session"
	}
	return prefix + "_" + hex.EncodeToString(sum[:])[:12]
}
