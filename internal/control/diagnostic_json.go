package control

import (
	"bytes"
	"encoding/json"

	"reasonix/internal/secrets"
	"reasonix/internal/session"
)

// Shared by live and cold exports: textual redaction of serialized JSON can
// turn numeric token counters into unquoted redaction markers.
func marshalDiagnosticCommit(commit session.Commit) ([]byte, error) {
	encoded, err := json.Marshal(commit)
	if err != nil {
		return nil, err
	}
	encoded, err = secrets.RedactJSON(encoded)
	if err != nil {
		return nil, err
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, encoded, "    ", "  "); err != nil {
		return nil, err
	}
	return formatted.Bytes(), nil
}
