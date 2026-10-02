package main

import (
	"testing"

	"reasonix/internal/testenv"
)

// run opens session services; one left open keeps its writer lease and recovery
// store, which only Windows reports as a t.TempDir cleanup failure.
func TestMain(m *testing.M) {
	testenv.RunWithLeaseGuard(m)
}
