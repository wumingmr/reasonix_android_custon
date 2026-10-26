package control

import "maps"

// SessionAuthorizations is the same-session tool-grant, external-folder read
// access, and Plan-mode command trust a controller rebuild must carry forward; see
// Controller.SessionAuthorizations / RestoreSessionAuthorizations.
type SessionAuthorizations struct {
	Grants                   []string
	PlanModeReadOnlyCommands []string
	WriteRoots               []string

	// Keep exact session-issued aliases private and detached from controller state.
	externalFolderRefs map[string]string
}

// Snapshots retain the original aliases and canonical roots: re-registering
// filesystem paths could resolve changed symlinks or mint different tokens.
func (c *Controller) snapshotExternalFolderRefs() map[string]string {
	c.externalFolderRefsMu.RLock()
	defer c.externalFolderRefsMu.RUnlock()
	return maps.Clone(c.externalFolderRefs)
}

func (c *Controller) restoreExternalFolderRefs(refs map[string]string) {
	if len(refs) == 0 {
		return
	}
	c.externalFolderRefsMu.Lock()
	if c.externalFolderRefs == nil {
		c.externalFolderRefs = make(map[string]string, len(refs))
	}
	maps.Copy(c.externalFolderRefs, refs)
	c.externalFolderRefsMu.Unlock()
	// Each replacement owns a fresh read-tool resolver. Controller aliases alone
	// are insufficient; carry the same read-only roots to the assembled tools.
	if c.externalFolderToolRefs != nil {
		for token, root := range refs {
			c.externalFolderToolRefs.RegisterReadRoot(token, root)
		}
	}
}
