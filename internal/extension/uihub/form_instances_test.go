package uihub

import (
	"context"
	"testing"

	"reasonix/internal/extension/protocol"
)

func publishTestForm(t *testing.T, h *Hub, handler UIHandler, surfaceID string) string {
	t.Helper()
	result, err := handler.Publish(context.Background(), protocol.UIPublishParams{
		SurfaceID: surfaceID, SessionID: "sess-1", Generation: 7, Kind: protocol.UISurfaceForm,
		Payload: mustRaw(t, protocol.UIFormPayload{Title: "Form", Fields: []protocol.UIFormField{}}),
	})
	if err != nil || !result.Accepted {
		t.Fatalf("publish form = %+v, %v", result, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.activeForms[formKey("alpha", surfaceID)].instanceID
}

func TestSubmitExactPinsInstanceAndRejectsDuplicate(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fake := &fakeActionClient{submitResult: protocol.UISubmitResult{Accepted: true}, submitStarted: started, submitRelease: release}
	h := New(Options{SessionID: "sess-1", Generation: 7, Resolve: func(string) ActionClient { return fake }})
	handler := h.HandlerFor("alpha")
	first := publishTestForm(t, h, handler, "f1")
	second := publishTestForm(t, h, handler, "f1")
	if second == first {
		t.Fatal("republishing the same surface reused its form instance")
	}
	if _, err := h.SubmitExact(context.Background(), "alpha", "f1", "sess-1", 7, first, nil); err == nil {
		t.Fatal("stale form instance reached the sidecar")
	}
	result := make(chan error, 1)
	go func() {
		_, err := h.SubmitExact(context.Background(), "alpha", "f1", "sess-1", 7, second, map[string]any{"v": 1})
		result <- err
	}()
	<-started
	if _, err := h.SubmitExact(context.Background(), "alpha", "f1", "sess-1", 7, second, nil); err == nil {
		t.Fatal("duplicate exact submission was accepted")
	}
	fake.mu.Lock()
	calls := len(fake.submitParams)
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("sidecar submit calls = %d, want 1", calls)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("exact submit: %v", err)
	}
}

func TestOldSubmitCompletionDoesNotDeleteReplacement(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fake := &fakeActionClient{submitResult: protocol.UISubmitResult{Accepted: true}, submitStarted: started, submitRelease: release}
	h := New(Options{SessionID: "sess-1", Generation: 7, Resolve: func(string) ActionClient { return fake }})
	handler := h.HandlerFor("alpha")
	oldInstance := publishTestForm(t, h, handler, "f1")
	done := make(chan error, 1)
	go func() {
		_, err := h.SubmitExact(context.Background(), "alpha", "f1", "sess-1", 7, oldInstance, nil)
		done <- err
	}()
	<-started
	newInstance := publishTestForm(t, h, handler, "f1")
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("old submit: %v", err)
	}
	h.mu.Lock()
	current, ok := h.activeForms[formKey("alpha", "f1")]
	h.mu.Unlock()
	if !ok || current.instanceID != newInstance {
		t.Fatalf("replacement form was removed by old completion: %+v, present=%v", current, ok)
	}
	if _, err := h.SubmitExact(context.Background(), "alpha", "f1", "sess-1", 7, newInstance, nil); err != nil {
		t.Fatalf("replacement form is not actionable: %v", err)
	}
}
