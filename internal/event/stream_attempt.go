package event

// StreamAttemptAction is the lifecycle phase of a local sampling attempt.
type StreamAttemptAction string

const (
	StreamAttemptBegin   StreamAttemptAction = "begin"
	StreamAttemptDiscard StreamAttemptAction = "discard"
	StreamAttemptCommit  StreamAttemptAction = "commit"
)

// StreamAttemptInfo carries host-local bookkeeping for one sampling attempt.
// Reason is a fixed enum (connection_reset | premature_eof | idle_timeout).
type StreamAttemptInfo struct {
	ID      string
	Action  StreamAttemptAction
	Attempt int // 1-based attempt number
	Max     int // total attempts including the first
	Reason  string
	// Response is persisted locally; it is not part of the frontend wire schema.
	Response *ResponseSummary
}
