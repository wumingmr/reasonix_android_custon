package main

import (
	"strings"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
)

func legacyCleanupArchivedSessions(state legacycleanup.State) map[string]legacycleanup.Candidate {
	items := map[string]legacycleanup.Candidate{}
	for _, item := range state.Items {
		if (item.Kind == "session" || item.Kind == "legacy") && item.Phase == "archived" && !item.Restored && item.SessionID != "" {
			items[item.SessionID] = item
		}
	}
	return items
}

func decorateLegacyCleanupTrashEntry(row *TrashEntry, batchID string, item legacycleanup.Candidate) {
	if row == nil || item.ID == "" {
		return
	}
	row.CleanupBatchID, row.CleanupKind = batchID, item.Kind
}

func legacyCleanupTopicTrashEntries(cleanup legacycleanup.State, workspaces workspacestate.State, query string) []TrashEntry {
	rows := []TrashEntry{}
	query = strings.ToLower(query)
	for _, item := range cleanup.Items {
		if item.Kind != "topic" || item.Phase != "archived" || item.Restored {
			continue
		}
		workspace := workspaces.Workspaces[item.WorkspaceID]
		row := TrashEntry{
			ID: item.ID, RecoveryEntryID: "legacy-cleanup:" + item.ID,
			Title: item.Title, WorkspaceID: item.WorkspaceID, WorkspaceTitle: workspace.Title,
			ArchivedAt: item.ArchivedAt, Health: "ready", CanRestore: true, CanPurge: true,
			CleanupBatchID: cleanup.BatchID, CleanupKind: "topic_placeholder",
		}
		if strings.Contains(strings.ToLower(row.Title+"\n"+row.WorkspaceTitle), query) {
			rows = append(rows, row)
		}
	}
	return rows
}
