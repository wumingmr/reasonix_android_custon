package main

import (
	"testing"

	"reasonix/internal/control"
)

// newFixtureController binds writable session ownership to the test lifetime.
// Register after creating the fixture directories so Close runs before their
// cleanup; Windows correctly refuses to remove an open ownership lock.
func newFixtureController(t *testing.T, opts control.Options) *control.Controller {
	t.Helper()
	ctrl := control.New(opts)
	t.Cleanup(ctrl.Close)
	return ctrl
}
