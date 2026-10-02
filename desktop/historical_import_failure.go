package main

import (
	"context"
	"errors"
	"unicode/utf8"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/session"
)

const historicalFailureDetailLimit = 2000

// historicalImportFailureCode projects the identity the producer attached to a
// preparation failure. It reads sentinels and typed errors only; a failure no
// producer classified stays import_failed and still carries its detail.
func historicalImportFailureCode(err error) string {
	var operationErr *SessionOperationError
	var transcriptErr *session.TranscriptInitializationError
	switch {
	case err == nil:
		return ""
	case historicalSourceBusyError(err):
		return "source_busy"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, errSessionWorkspaceConflict):
		return "workspace_conflict"
	case errors.Is(err, agent.ErrSessionReplayLimitExceeded):
		return "history_too_large"
	case errors.Is(err, agent.ErrSessionHistoryDamaged), errors.Is(err, session.ErrDamagedStore), errors.As(err, &transcriptErr):
		return "history_damaged"
	case errors.Is(err, session.ErrUnsupportedVersion):
		return "unsupported_version"
	case errors.Is(err, workspacestate.ErrMutationConflict), errors.Is(err, session.ErrImportConflict):
		return "state_conflict"
	case errors.As(err, &operationErr) && operationErr.Code != "":
		return operationErr.Code
	default:
		return "import_failed"
	}
}

// historicalImportFailureDetail is display text for the local user, bounded so
// a failure that quotes imported content cannot flood the surface.
func historicalImportFailureDetail(err error) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	detail := err.Error()
	if len(detail) <= historicalFailureDetailLimit {
		return detail
	}
	cut := historicalFailureDetailLimit
	for cut > 0 && !utf8.RuneStart(detail[cut]) {
		cut--
	}
	return detail[:cut] + "…"
}
