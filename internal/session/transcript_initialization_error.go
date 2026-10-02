package session

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"

	"reasonix/internal/transcript"
)

// TranscriptInitializationError crosses service and migration boundaries
// without losing the original cause. LogValue never emits raw error text,
// session paths, provider metadata, or imported record identities.
type TranscriptInitializationError struct {
	sessionID     string
	covered       uint64
	messageCount  int
	totalMessages int
	cause         error
}

func (e *TranscriptInitializationError) Error() string {
	return fmt.Sprintf("session: initialize transcript for %q: %v", e.sessionID, e.cause)
}

func (e *TranscriptInitializationError) Unwrap() error { return e.cause }

// Classification returns a content-free reason suitable for aggregate crash
// diagnostics. Detailed correlation identifiers remain local-only in LogValue.
func (e *TranscriptInitializationError) Classification() string {
	var baseline *transcript.BaselineError
	if errors.As(e.cause, &baseline) {
		return baseline.Code()
	}
	return "transcript_initialization_failed"
}

func (e *TranscriptInitializationError) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.Int("version", 1),
		slog.String("code", "transcript_initialization_failed"),
		slog.String("session_key", fmt.Sprintf("%x", sha256.Sum256([]byte(e.sessionID)))),
		slog.Uint64("covered_sequence", e.covered),
		slog.Int("baseline_message_count", e.messageCount),
		slog.Int("baseline_total_message_count", e.totalMessages),
	}
	var baseline *transcript.BaselineError
	if errors.As(e.cause, &baseline) {
		attrs = append(attrs, slog.Any("baseline", baseline))
	}
	return slog.GroupValue(attrs...)
}
