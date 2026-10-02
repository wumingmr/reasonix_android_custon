package skill

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestCatalogWatchDrainsBackendErrorsDuringRegistrationAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan fsnotify.Event)
	backendErrors := make(chan error)
	ready, done := make(chan struct{}), make(chan struct{})
	registering := make(chan struct{}, 1)
	release := make(chan struct{})
	register := func() {
		registering <- struct{}{}
		<-release
		// Model inotify holding its backend mutex while reporting an error.
		// Add cannot finish until the event consumer accepts this send.
		backendErrors <- errors.New("directory renamed while registering")
	}
	closeBackend := func() error {
		backendErrors <- errors.New("backend closing")
		close(events)
		close(backendErrors)
		return nil
	}
	go func() {
		defer close(done)
		runCatalogWatch(ctx, events, backendErrors, ready, register, closeBackend, func(string) {})
	}()
	select {
	case <-registering:
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not start")
	}
	// Cancel while Add is blocked: the consumer must still drain backend
	// errors before the worker can finish registration and close the backend.
	cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher stopped draining errors during shutdown")
	}
	select {
	case <-ready:
	default:
		t.Fatal("initial registration did not finish")
	}
}
