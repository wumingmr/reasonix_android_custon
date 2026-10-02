package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"reasonix/internal/secrets"
	"reasonix/internal/session"
)

// WriteColdSessionDiagnostics serializes the durable evidence of a fixed
// snapshot without starting or taking ownership of a session runtime.
func WriteColdSessionDiagnostics(ctx context.Context, dst io.Writer, query *session.Query, snapshot session.ExportSnapshot, metadata GoalDiagnosticMetadata, extra map[string]any) error {
	if query == nil || snapshot.Ref.SessionID == "" {
		return errors.New("cold session diagnostics require a canonical snapshot")
	}
	if err := query.ValidateExportSourceForRef(snapshot.Ref, snapshot); err != nil {
		return err
	}
	if metadata.Capabilities == nil {
		metadata.Capabilities = []string{}
	}
	fillGoalDiagnosticBuildMetadata(&metadata)
	unavailable := []string{"runtime, submissionDiagnostics, shellDiagnostics and process-local lifecycle history are unavailable for a cold session"}
	if snapshot.ReadIncomplete {
		unavailable = append(unavailable, "snapshot capture encountered unreadable durable events; only the readable prefix is available")
	}
	if _, err := io.WriteString(dst, "{\n"); err != nil {
		return err
	}
	fields := []struct {
		name  string
		value any
	}{
		{"schemaVersion", 1},
		{"exportedAt", snapshot.CapturedAt},
		{"metadata", metadata},
		{"runtime", nil},
		{"observation", nil},
		{"submissionDiagnostics", nil},
		{"shellDiagnostics", nil},
		{"lifecycleDiagnostics", nil},
	}
	for _, field := range fields {
		if err := writeGoalDiagnosticField(dst, field.name, field.value, true); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		encoded, err := json.Marshal(extra[name])
		if err != nil {
			return err
		}
		redacted := json.RawMessage(secrets.Redact(string(encoded)))
		if !json.Valid(redacted) {
			return errors.New("invalid redacted diagnostic field")
		}
		if err = writeGoalDiagnosticField(dst, name, redacted, true); err != nil {
			return err
		}
	}
	through, traversalErr, err := writeColdDiagnosticCommits(ctx, dst, query, snapshot)
	if err != nil {
		return err
	}
	if traversalErr != nil {
		unavailable = append(unavailable, "durable event traversal failed: "+secrets.RedactError(traversalErr))
	}
	if err = writeGoalDiagnosticField(dst, "activationChanges", []goalDiagnosticTransition{}, true); err != nil {
		return err
	}
	if err = writeGoalDiagnosticField(dst, "acceptedThrough", through, true); err != nil {
		return err
	}
	if err = writeGoalDiagnosticField(dst, "durableThrough", through, true); err != nil {
		return err
	}
	if err = writeGoalDiagnosticField(dst, "unavailable", unavailable, false); err != nil {
		return err
	}
	_, err = io.WriteString(dst, "}\n")
	return err
}

func writeColdDiagnosticCommits(ctx context.Context, dst io.Writer, query *session.Query, snapshot session.ExportSnapshot) (through uint64, traversalErr, err error) {
	if _, err = io.WriteString(dst, "  \"commits\": ["); err != nil {
		return 0, nil, err
	}
	first := true
	var destinationError error
	traversalErr = query.StreamExportCommits(ctx, snapshot, func(commit session.Commit) error {
		if commit.LastSequence() > snapshot.SnapshotSequence {
			return nil
		}
		encoded, marshalErr := json.MarshalIndent(commit, "    ", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		encoded = []byte(secrets.Redact(string(encoded)))
		if !json.Valid(encoded) {
			return errors.New("redacted cold diagnostic commit is not valid JSON")
		}
		separator := "\n    "
		if !first {
			separator = ",\n    "
		}
		if _, writeErr := io.WriteString(dst, separator); writeErr != nil {
			destinationError = writeErr
			return writeErr
		}
		if _, writeErr := dst.Write(encoded); writeErr != nil {
			destinationError = writeErr
			return writeErr
		}
		first = false
		through = commit.LastSequence()
		return nil
	})
	if destinationError != nil {
		return 0, nil, destinationError
	}
	if err = ctx.Err(); err != nil {
		return 0, nil, err
	}
	if !first {
		if _, err = io.WriteString(dst, "\n  "); err != nil {
			return 0, nil, err
		}
	}
	if _, err = io.WriteString(dst, "],\n"); err != nil {
		return 0, nil, err
	}
	return through, traversalErr, nil
}
