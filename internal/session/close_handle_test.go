package session

import (
	"context"
	"path/filepath"
	"testing"
)

type gatedCloseHandle struct {
	SessionHandle
	entered chan struct{}
	release chan struct{}
}

func (h *gatedCloseHandle) Close(ctx context.Context) error {
	close(h.entered)
	<-h.release
	return h.SessionHandle.Close(ctx)
}

func TestSessionHandleRemainsStableDuringClose(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "session"), "closing")
	if err != nil {
		t.Fatal(err)
	}
	handle := &gatedCloseHandle{SessionHandle: s.Handle(), entered: make(chan struct{}), release: make(chan struct{})}
	s.binding.handle = handle // Install before publishing the test session to goroutines.
	done := make(chan error, 1)
	go func() { done <- s.Close(context.Background()) }()
	<-handle.entered
	got := s.Handle()
	state := s.StateSnapshot()
	close(handle.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got != handle || s.Handle() != handle {
		t.Fatal("close changed the physical handle visible to concurrent readers")
	}
	if state.EventSequence != 0 {
		t.Fatalf("close changed the accepted sequence: %d", state.EventSequence)
	}
	if _, err := s.Append(context.Background(), Batch{OperationID: "late", Events: []Event{{Kind: "turn/start"}}}); err == nil {
		t.Fatal("retaining the handle reopened write admission")
	}
}
