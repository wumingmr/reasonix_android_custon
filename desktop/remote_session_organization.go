package main

import (
	"encoding/json"
	"fmt"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"slices"
)

// Organization belongs to the existing desktop remote-settings owner. Reads
// never start Serve or open a connection. Host and workspace both scope the CAS.
func (a *App) remoteSessionOrganization(w SessionOrganizationWorkspace, revision *uint64, mutation *SessionOrganizationMutation) (SessionOrganizationSnapshot, error) {
	var result SessionOrganizationSnapshot
	keyOf := func(selector *SessionSelector) (string, error) {
		if selector == nil {
			return "", fmt.Errorf("session target is required")
		}
		if selector.Ref != nil && selector.Ref.HostID == w.HostID && selector.Ref.SessionID != "" {
			return "ref\x00" + w.HostID + "\x00" + selector.Ref.SessionID, nil
		}
		if selector.Source != nil && selector.Source.HostID == w.HostID && selector.Source.Path != "" {
			return "source\x00" + w.HostID + "\x00" + w.WorkspaceRoot + "\x00" + selector.Source.Path, nil
		}
		return "", newSessionOperationError("target_changed", "The session belongs to a different host.")
	}
	key, anchor := "", ""
	if mutation != nil && (mutation.Kind == "move" || mutation.Kind == "set-group") {
		var err error
		key, err = keyOf(mutation.Target)
		if err != nil {
			return result, err
		}
		if mutation.Kind == "move" {
			anchor, err = keyOf(mutation.Anchor)
			if err != nil {
				return result, err
			}
		}
		rows, err := a.RemoteProjectSessions(w.HostID, w.WorkspaceRoot)
		if err != nil {
			return result, err
		}
		known := map[string]bool{}
		for _, row := range rows {
			if row.SessionID != "" {
				known["ref\x00"+w.HostID+"\x00"+row.SessionID] = true
			}
			known["source\x00"+w.HostID+"\x00"+w.WorkspaceRoot+"\x00"+row.Path] = true
		}
		if !known[key] || (anchor != "" && !known[anchor]) {
			return result, newSessionOperationError("target_not_found", "The remote session is no longer available.")
		}
	}
	read := func(c *config.Config) (bool, error) {
		for i := range c.Remote.Projects {
			p := &c.Remote.Projects[i]
			if p.HostID != w.HostID || p.Workspace != w.WorkspaceRoot {
				continue
			}
			var changed bool
			var err error
			result, changed, err = mutateRemoteOrganization(p, revision, mutation, key, anchor)
			return changed, err
		}
		return false, newSessionOperationError("target_not_found", "This remote workspace is no longer pinned.")
	}
	if mutation == nil {
		c, err := config.Load()
		if err != nil {
			return result, err
		}
		_, err = read(c)
		return result, err
	}
	err := editUserConfigIfChanged(read)
	if err == nil && result.Applied {
		a.emitProjectTreeMetadataChanged()
	}
	return result, err
}

func mutateRemoteOrganization(p *config.RemoteProjectEntry, revision *uint64, mutation *SessionOrganizationMutation, key, anchor string) (SessionOrganizationSnapshot, bool, error) {
	o := workspacestate.Organization{Order: []string{}, Groups: []workspacestate.OrganizationGroup{}, Imported: map[string]bool{}, MigrationVersion: 1}
	if p.SessionOrganization != "" {
		if err := json.Unmarshal([]byte(p.SessionOrganization), &o); err != nil {
			return SessionOrganizationSnapshot{}, false, err
		}
	}
	if o.Imported == nil {
		o.Imported = map[string]bool{}
	}
	result := organizationSnapshot(o, mutation == nil)
	if mutation == nil || revision == nil || o.Revision != *revision {
		return result, false, nil
	}
	for _, k := range []string{key, anchor} {
		if k != "" {
			o.Imported[k] = true
			if !slices.Contains(o.Order, k) {
				o.Order = append(o.Order, k)
			}
		}
	}
	if err := applyOrganizationMutation(&o, *mutation, key, anchor); err != nil {
		return SessionOrganizationSnapshot{}, false, err
	}
	o.Revision++
	body, err := json.Marshal(o)
	if err != nil {
		return SessionOrganizationSnapshot{}, false, err
	}
	p.SessionOrganization = string(body)
	result = organizationSnapshot(o, true)
	return result, true, nil
}
