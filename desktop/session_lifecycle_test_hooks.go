package main

import "context"

// appLifecycleTestHooks exposes instance-local transaction boundaries to tests.
// Set hooks before starting operations and never change them concurrently.
type appLifecycleTestHooks struct {
	runtimeMutationBeforeLockHook func(string)
	lifecycleCheckpointHook       func(string)
	sessionOpenBuildHook          func(context.Context)
}
