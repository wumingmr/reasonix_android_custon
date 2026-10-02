package main

import (
	"errors"
	"strings"

	"reasonix/internal/control"
)

// errPermissionSessionChanged refuses a preset choice read from a session the
// tab no longer shows; the bridge carries it as reasonix_error:permission_session_changed.
var errPermissionSessionChanged = errors.New("permission choice was made in another session")

const permissionSessionChangedCode = "permission_session_changed"

func requirePermissionSession(live control.PermissionSnapshot, expectedSessionID string) error {
	expected := strings.TrimSpace(expectedSessionID)
	if expected != "" && expected == strings.TrimSpace(live.SessionID) {
		return nil
	}
	return &inboxCodedError{code: permissionSessionChangedCode, cause: errPermissionSessionChanged}
}
