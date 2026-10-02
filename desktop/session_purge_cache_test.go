package main

import (
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestArchiveRetiresCachedCanonicalRuntime(t *testing.T) {
	a, ref := lifecycleFixture(t)
	service := a.desktopSessionService("")
	binding, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, cached := service.Runtime(ref); !cached {
		t.Fatal("fixture must retain an unbound cached runtime")
	}
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if _, cached := service.Runtime(ref); cached {
		t.Fatal("archived runtime retained its writer lease in the idle cache")
	}
	if err := a.PurgeCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeRetiresOnlyUnboundArchivedRuntime(t *testing.T) {
	a, ref := lifecycleFixture(t)
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	service := a.desktopSessionService("")
	binding, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Release(t.Context()) })
	if err := a.PurgeCanonicalSession(ref); err == nil {
		t.Fatal("purge closed a runtime still bound to a client")
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil || state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Archived {
		t.Fatalf("bound purge changed lifecycle: %+v %v", state, err)
	}
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.PurgeCanonicalSession(ref); err != nil {
		t.Fatalf("unbound cache blocked purge: %v", err)
	}
}
