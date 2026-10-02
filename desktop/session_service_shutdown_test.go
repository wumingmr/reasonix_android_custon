package main

import (
	"context"
	"path/filepath"
	"testing"

	"reasonix/internal/session"
)

func TestShutdownReleasesCachedSessionOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	app := NewApp()
	service := app.desktopSessionService(dir)
	if service == nil {
		t.Fatal("missing session service")
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "shutdown-cache"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.Bind(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	app.shutdown(context.Background())
	if _, ok := service.Runtime(runtime.Ref()); ok {
		t.Fatal("shutdown retained the idle runtime")
	}
	reopened, err := session.NewService("local", session.NewFilesystemPersistence(app.desktopSessions.root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.CloseAll(context.Background()) })
	reader, err := reopened.EnsureExecution(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatalf("shutdown retained the writer lease: %v", err)
	}
	if err := reader.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := reopened.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}
