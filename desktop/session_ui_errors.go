package main

import (
	"errors"
	"reasonix/desktop/internal/sessionui"
)

func sessionUIError(err error, target, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sessionui.ErrConflict) {
		return &SessionOperationError{Code: "input_conflict", Message: "The input changed while saving. Please retry.", TargetKey: target, OperationID: operation}
	}
	if errors.Is(err, sessionui.ErrFutureVersion) {
		return &SessionOperationError{Code: "unsupported_ui_schema", Message: "This input database requires a newer application. Its data has not been changed.", TargetKey: target, OperationID: operation}
	}
	return sessionOperationErrorForTarget(err, target, operation)
}
