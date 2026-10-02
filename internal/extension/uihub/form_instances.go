package uihub

import (
	"context"
	"strings"

	"reasonix/internal/extension/protocol"
)

type activeForm struct {
	pluginID   string
	surfaceID  string
	sessionID  string
	generation uint64
	instanceID string
	submitting bool
}

func formKey(pluginID, surfaceID string) string { return pluginID + "\x00" + surfaceID }

// SubmitExact pins the form instance and sidecar client while holding the hub
// lock. The sidecar call runs after unlock, so replacements keep their identity.
func (h *Hub) SubmitExact(ctx context.Context, pluginID, surfaceID, sessionID string, generation uint64, instanceID string, values map[string]any) (protocol.UISubmitResult, error) {
	if strings.TrimSpace(surfaceID) == "" || strings.TrimSpace(instanceID) == "" {
		return protocol.UISubmitResult{}, &protocol.ProtocolError{Reason: protocol.ErrInvalidParams, Message: "exact form identity is required"}
	}
	h.mu.Lock()
	form, ok := h.activeForms[formKey(pluginID, surfaceID)]
	if !ok || form.sessionID != sessionID || form.generation != generation || form.instanceID != instanceID {
		h.mu.Unlock()
		return protocol.UISubmitResult{}, &protocol.ProtocolError{Reason: protocol.ErrInvalidParams, Message: "extension form is stale"}
	}
	if form.submitting {
		h.mu.Unlock()
		return protocol.UISubmitResult{}, &protocol.ProtocolError{Reason: protocol.ErrInvalidParams, Message: "extension form submission is already in progress"}
	}
	if !h.known[pluginID] {
		h.mu.Unlock()
		return protocol.UISubmitResult{}, unknownClientError(pluginID)
	}
	if h.crashed[pluginID] || h.resolve == nil {
		h.mu.Unlock()
		return protocol.UISubmitResult{}, &protocol.ProtocolError{Reason: protocol.ErrProviderInterrupted, Message: "extension sidecar is unavailable"}
	}
	client := h.resolve(pluginID)
	if client == nil {
		h.mu.Unlock()
		return protocol.UISubmitResult{}, &protocol.ProtocolError{Reason: protocol.ErrProviderInterrupted, Message: "extension " + pluginID + " has no live sidecar"}
	}
	form.submitting = true
	h.activeForms[formKey(pluginID, surfaceID)] = form
	h.mu.Unlock()
	result, err := client.UISubmit(ctx, protocol.UISubmitParams{SurfaceID: surfaceID, SessionID: sessionID, Generation: generation, Values: values})
	h.mu.Lock()
	current, currentOK := h.activeForms[formKey(pluginID, surfaceID)]
	if currentOK && current.instanceID == instanceID {
		if err != nil || result.Accepted {
			delete(h.activeForms, formKey(pluginID, surfaceID))
		} else {
			current.submitting = false
			h.activeForms[formKey(pluginID, surfaceID)] = current
		}
	}
	h.mu.Unlock()
	return result, err
}
