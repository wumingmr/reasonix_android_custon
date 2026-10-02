package transcript

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
)

// BaselineError retains the original error for callers and exposes only
// bounded, content-free diagnostics to structured loggers. Record identities
// can originate in imported data, so even those are logged as fingerprints.
type BaselineError struct {
	cause         error
	code          string
	recordCount   int
	index         int
	previousIndex int
	record        Message
}

func (e *BaselineError) Error() string { return e.cause.Error() }
func (e *BaselineError) Unwrap() error { return e.cause }

// Code returns the bounded failure classification used by local diagnostics
// and crash aggregation. It never contains transcript or record data.
func (e *BaselineError) Code() string { return e.code }

func (e *BaselineError) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("code", e.code), slog.Int("record_count", e.recordCount)}
	if e.index >= 0 {
		attrs = append(attrs, slog.Int("record_index", e.index), slog.String("role", diagnosticRole(e.record.Role)),
			slog.String("record_key", diagnosticKey(e.record.RecordID)),
			slog.String("message_key", diagnosticKey(e.record.MessageID)),
			slog.String("tool_call_key", diagnosticKey(e.record.ToolCallID)))
	}
	if e.previousIndex >= 0 {
		attrs = append(attrs, slog.Int("previous_record_index", e.previousIndex))
	}
	return slog.GroupValue(attrs...)
}

func newBaselineError(cause error, code string, count, index, previous int, record Message) error {
	// Do not retain chat bodies or nested metadata through a returned error.
	return &BaselineError{cause: cause, code: code, recordCount: count, index: index, previousIndex: previous,
		record: Message{Role: record.Role, RecordID: record.RecordID, MessageID: record.MessageID, ToolCallID: record.ToolCallID}}
}

func diagnosticKey(identity string) string {
	if identity == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

func diagnosticRole(role string) string {
	switch role {
	case "system", "user", "assistant", "tool", "notice":
		return role
	default:
		return "other"
	}
}
