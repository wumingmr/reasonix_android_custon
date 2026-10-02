package main

import (
	"reasonix/internal/control"
	"testing"
	"time"
)

func closeRemoteTestController(t *testing.T, ctrl *control.Controller) {
	t.Helper()
	ctrl.Close()
	select {
	case <-ctrl.Closed():
	case <-time.After(10 * time.Second):
		t.Error("controller teardown did not finish before temporary-directory cleanup")
	}
}
