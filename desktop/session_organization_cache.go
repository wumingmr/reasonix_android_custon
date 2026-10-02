package main

import (
	"crypto/sha256"
	"encoding/json"
	"sync"

	"reasonix/desktop/internal/workspacestate"
)

type organizationReadCache struct {
	mu      sync.Mutex
	entries map[[32]byte]workspacestate.Organization
}

func (c *organizationReadCache) get(key [32]byte) (workspacestate.Organization, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.entries[key]
	return o.Clone(), ok
}

func (c *organizationReadCache) put(key [32]byte, o workspacestate.Organization) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= 32 {
		c.entries = map[[32]byte]workspacestate.Organization{}
	}
	c.entries[key] = o.Clone()
}

func (a *App) organizationImportKey(scope, root string, state workspacestate.State, id string, projects any) ([32]byte, bool) {
	var zero [32]byte
	catalog := a.sessionCatalog.Load()
	if catalog == nil {
		return zero, false
	}
	availability := a.catalogWorkspaceAvailability(catalog, scope, root)
	if !availability.usable || !availability.complete {
		return zero, false
	}
	body, err := json.Marshal([]any{state.Workspaces[id], state.SourceMappings, state.Presentation, projects, a.currentSessionCatalogStatus().Revision})
	return sha256.Sum256(body), err == nil
}
