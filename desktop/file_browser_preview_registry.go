package main

import (
	"errors"
	"sync"
)

// Lifecycle cleanup takes only mu, never operations. Browser RPC may reenter
// App, so operations must never be acquired while App.mu is held.
type fileBrowserPreviewRegistry struct {
	mu         sync.Mutex
	operations sync.Mutex
	bindings   map[string]fileBrowserPreviewBinding
	pending    *fileBrowserPreviewOperation
}

type fileBrowserPreviewOperation struct {
	key         string
	preview     fileBrowserPreviewBinding
	previousURL string
	revoked     bool // guarded by registry.mu
}

func (r *fileBrowserPreviewRegistry) begin(key string, prepared preparedFileBrowserPreview) (*fileBrowserPreviewOperation, fileBrowserPreviewBinding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	binding, reusable := r.bindings[key]
	op := &fileBrowserPreviewOperation{key: key, previousURL: binding.URL,
		preview: fileBrowserPreviewBinding{BrowserTabID: binding.BrowserTabID, URL: prepared.url, release: prepared.release}}
	r.pending = op
	return op, binding, reusable
}

func (r *fileBrowserPreviewRegistry) end(op *fileBrowserPreviewOperation) {
	r.mu.Lock()
	if r.pending == op {
		r.pending = nil
	}
	r.mu.Unlock()
}

func (r *fileBrowserPreviewRegistry) retire(key, rawURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if binding, ok := r.bindings[key]; ok && binding.URL == rawURL {
		delete(r.bindings, key)
		binding.release()
	}
}

// Publish under the same App -> registry order as tab removal. A late host
// reply can neither resurrect a removed binding nor attach to a new session.
func (a *App) publishFileBrowserPreview(tab *WorkspaceTab, generation uint64, key string, op *fileBrowserPreviewOperation, binding fileBrowserPreviewBinding) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	r := &a.filePreviews
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.tabs[tab.ID] != tab || tab.removed || tab.SessionGeneration != generation || op.revoked {
		return errors.New("the session changed before the preview opened")
	}
	if r.bindings == nil {
		r.bindings = make(map[string]fileBrowserPreviewBinding)
	}
	r.bindings[key] = binding
	return nil
}

// Release handles were captured when this app minted the resource. Revoking
// them is in-memory and cannot reacquire App.mu or wait for a browser request.
func (r *fileBrowserPreviewRegistry) releaseMatching(match func(string, fileBrowserPreviewBinding) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, binding := range r.bindings {
		if match(key, binding) {
			delete(r.bindings, key)
			if binding.release != nil {
				binding.release()
			}
		}
	}
	if op := r.pending; op != nil {
		previous := op.preview
		previous.URL = op.previousURL
		if match(op.key, op.preview) || match(op.key, previous) {
			op.revoked = true
			op.preview.release()
		}
	}
}
