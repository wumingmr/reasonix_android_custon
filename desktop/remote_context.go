package main

import "encoding/json"

// remoteContextSnapshot reads the selected remote session, then rejects a
// response that raced with a reconnect or a switch to another session.
func (a *App) remoteContextSnapshot(tabID string) (int, int, bool) {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	if tab == nil || tab.client == nil || tab.state != "ready" || tab.routing.rehydratingPath != "" {
		a.remoteTabMu.Unlock()
		return 0, 0, false
	}
	client, base := tab.client, tab.base
	path, generation, revision := tab.routing.currentPath, tab.gen, tab.routing.pathRevision
	a.remoteTabMu.Unlock()

	ctx, cancel := commandContext(a)
	defer cancel()
	data, err := serveGet(ctx, client, serveURL(base, "/context"), path)
	if err != nil {
		return 0, 0, false
	}
	var value struct {
		Used   int `json:"used"`
		Window int `json:"window"`
	}
	if json.Unmarshal(data, &value) != nil {
		return 0, 0, false
	}
	a.remoteTabMu.Lock()
	current := a.remoteTabs[tabID]
	valid := current == tab && current.client == client && current.gen == generation &&
		current.routing.pathRevision == revision && current.routing.currentPath == path &&
		current.routing.rehydratingPath == "" && current.state == "ready"
	a.remoteTabMu.Unlock()
	return value.Used, value.Window, valid
}
