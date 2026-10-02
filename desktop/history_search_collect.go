package main

import (
	"strings"
)

func historySearchRootFilter(a *App, req HistorySearchRequest) []string {
	rootFilter := []string{}
	for _, root := range historyCatalogRoots(a.sessionCatalogTargets()) {
		if req.Scope == "project" && (root.Scope != "project" || !sameProjectRoot(root.WorkspaceRoot, req.WorkspaceRoot)) {
			continue
		}
		if req.Scope == "global" && root.Scope == "project" {
			continue
		}
		rootFilter = append(rootFilter, root.Path)
	}
	return rootFilter
}

func historyStatusMatches(status string, open, current bool) bool {
	switch strings.TrimSpace(status) {
	case "open":
		// Match HistoryPanel: open-but-not-current.
		return open && !current
	case "current":
		return current
	default:
		return true
	}
}
