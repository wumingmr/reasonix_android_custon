package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const compactionBudget = 5 * time.Minute

// SummaryError keeps the failure identity without changing the wire contract.
type SummaryError struct {
	Code  string
	Cause error
}

func (e *SummaryError) Error() string { return fmt.Sprintf("%s: %v", e.Code, e.Cause) }
func (e *SummaryError) Unwrap() error { return e.Cause }

var errSummaryEmpty = errors.New("summarizer returned empty output")
var errSummaryBudget = &SummaryError{Code: "summary_budget_exceeded", Cause: context.DeadlineExceeded}

func summaryError(err error) error {
	if err == nil {
		return nil
	}
	var typed *SummaryError
	var persistence *compactionPersistenceError
	if errors.As(err, &typed) || errors.As(err, &persistence) || errors.Is(err, context.Canceled) {
		return err
	}
	var code string
	switch {
	case errors.Is(err, errSummaryEmpty):
		code = "summary_empty"
	case errors.Is(err, errSummaryOutputTruncated):
		code = "summary_output_truncated"
	case errors.Is(err, errCheckpointRejected):
		code = "summary_no_reduction"
	case errors.Is(err, errCompressStaleContext):
		code = "summary_context_changed"
	default:
		return err
	}
	return &SummaryError{Code: code, Cause: err}
}

// Only the summary request owner may classify an otherwise untyped provider
// failure. Entry-point validation, extensions, and persistence keep their errors.
func summaryRequestError(ctx context.Context, err error) error {
	err = summaryError(compactionError(ctx, err))
	var typed *SummaryError
	var persistence *compactionPersistenceError
	if err == nil || errors.As(err, &typed) || errors.As(err, &persistence) || errors.Is(err, context.Canceled) {
		return err
	}
	return &SummaryError{Code: "summary_provider_error", Cause: err}
}

type compactionRunKey struct{}
type compactionRun struct{ work context.Context }

func currentCompactionRun(ctx context.Context) *compactionRun {
	run, _ := ctx.Value(compactionRunKey{}).(*compactionRun)
	return run
}

// Every nested summary, replan, and chunk inherits the same deadline. The
// caller's ordinary answer context is not cancelled by this work budget.
func (a *Agent) beginCompactionRun(parent context.Context) (context.Context, func(error) error) {
	if parent == nil {
		parent = context.Background()
	}
	if run := currentCompactionRun(parent); run != nil {
		return run.work, func(err error) error { return err }
	}
	ctx, cancel := context.WithTimeoutCause(parent, compactionBudget, errSummaryBudget)
	run := &compactionRun{}
	ctx = context.WithValue(ctx, compactionRunKey{}, run)
	run.work = ctx
	return ctx, func(err error) error {
		defer cancel()
		return compactionError(ctx, err)
	}
}

func compactionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var persistence *compactionPersistenceError
	if errors.As(err, &persistence) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) && errors.Is(context.Cause(ctx), errSummaryBudget) && !errors.Is(err, errSummaryBudget) {
		if err == context.DeadlineExceeded { //nolint:errorlint // Only the bare sentinel can be replaced without discarding an enclosing error.
			return errSummaryBudget
		}
		// Keep any enclosing recovery/error identity, even when a work deadline
		// needs its budget classification added. An expired run alone is not
		// evidence that a later request-preparation error came from compaction.
		return fmt.Errorf("%w: %w", err, errSummaryBudget)
	}
	return err
}

type compactionPersistenceError struct{ error }

func (e *compactionPersistenceError) Unwrap() error { return e.error }
