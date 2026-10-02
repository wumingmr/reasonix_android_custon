package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
)

func rewriteHistoricalSourceIdentityForTest(t *testing.T, app *App, key string) string {
	t.Helper()
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping, committed := state.SourceMappings[key]
	if !committed {
		for _, op := range state.PendingOperations {
			if op.Mapping != nil && op.Mapping.SourceKey == key {
				mapping = *op.Mapping
				break
			}
		}
	}
	if mapping.Path == "" {
		t.Fatal("fixture has no source receipt")
	}
	oldKey := fmt.Sprintf("%x", sha256.Sum256([]byte(mapping.Path+"\x00")))
	if oldKey == key { // Case-sensitive CI still exercises a changed persisted identity.
		oldKey = fmt.Sprintf("%x", sha256.Sum256([]byte("previous-spelling:"+mapping.Path+"\x00")))
	}
	if committed {
		delete(state.SourceMappings, key)
		mapping.SourceKey = oldKey
		state.SourceMappings[oldKey] = mapping
	}
	for id, op := range state.PendingOperations {
		if op.Mapping != nil && op.Mapping.SourceKey == key {
			copy := *op.Mapping
			copy.SourceKey = oldKey
			op.Mapping = &copy
			state.PendingOperations[id] = op
		}
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := app.workspaceRegistry().Path()
	if err := os.WriteFile(registryPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	return oldKey
}

func TestHistoricalSourceIdentityUpgradeKeepsOneConversation(t *testing.T) {
	isolateDesktopUserDirs(t)
	const id = "MixedCase-history"
	coldV4MigrationFixture(t, config.SessionStoreDir(), id)
	app := newHistoricalLifecycleApp(t)
	key := historicalLifecycleID(t, app, id)
	imported, err := app.ImportHistoricalSession(key)
	if err != nil {
		t.Fatal(err)
	}
	app.stopHistoricalImports()
	app.closeSessionServices()
	oldKey := rewriteHistoricalSourceIdentityForTest(t, app, key)
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping := state.SourceMappings[oldKey]
	for range 2 {
		app = newHistoricalLifecycleApp(t)
		listed, err := app.ListHistoricalSessions()
		if err != nil || len(listed.Items) != 1 || listed.Items[0].Session == nil || *listed.Items[0].Session != imported.Session {
			t.Fatalf("discovery lost adoption: %+v %v", listed, err)
		}
		page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
		if err != nil || len(page.Items) != 1 || page.Items[0].Session == nil || *page.Items[0].Session != imported.Session {
			t.Fatalf("duplicate sidebar rows: %+v %v", page.Items, err)
		}
		if !slices.Contains(page.Items[0].IdentityAliases, "source\x00local\x00"+key) {
			t.Fatal("current source row cannot merge with canonical identity")
		}
		for _, supplied := range []string{oldKey, key} {
			target, err := app.resolveSessionTarget(SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, SourceKey: supplied, Path: mapping.Path}})
			if err != nil || target.SessionRef != imported.Session {
				t.Fatalf("source %q opened another target: %+v %v", supplied, target, err)
			}
			preparedKey, _, err := app.historicalSourceForSelector(SessionSelector{Source: &SessionSourceRef{SourceKey: supplied, Path: mapping.Path}})
			if err != nil || preparedKey != oldKey {
				t.Fatalf("preparation changed durable identity: %s %v", preparedKey, err)
			}
		}
		if _, err := app.resolveSessionTarget(SessionSelector{Source: &SessionSourceRef{SourceKey: oldKey, Path: filepath.Join(mapping.Path, "different")}}); err == nil {
			t.Fatal("old source key authorized a different path")
		}
		ref, adopted, err := app.legacyCanonicalRef(t.Context(), mapping.Path)
		if err != nil || !adopted || ref != imported.Session {
			t.Fatalf("saved path failed canonical binding: %+v %v", ref, err)
		}
		view, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		saved := desktopTabEntry{Scope: "global", SessionPath: mapping.Path}
		if app.savedTabHistoricalSource(saved, savedTabReconcileEvidence{registry: view}) != nil {
			t.Fatal("restored canonical tab became a historical shell")
		}
		again, err := app.ImportHistoricalSession(key)
		if err != nil || again.Session != imported.Session {
			t.Fatalf("reimport duplicated identity: %+v %v", again, err)
		}
		after, err := app.workspaceRegistry().Load(t.Context())
		if err != nil || !reflect.DeepEqual(after.SourceMappings, state.SourceMappings) || !reflect.DeepEqual(after.PendingOperations, state.PendingOperations) || !reflect.DeepEqual(after.SessionStates, state.SessionStates) {
			t.Fatal("compatibility lookup changed adoption evidence or session lifecycles")
		}
		if len(after.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs) != 1 {
			t.Fatal("reimport added a second conversation")
		}
		app.desktopSessions.readSnapshots.close()
		app.stopHistoricalImports()
		app.closeSessionServices()
	}
	// One lifecycle owns both source spellings, including after a cold start.
	app = newHistoricalLifecycleApp(t)
	assertRows := func(want int) {
		t.Helper()
		if _, err := app.ListHistoricalSessions(); err != nil {
			t.Fatal(err)
		}
		page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
		if err != nil || len(page.Items) != want {
			t.Fatalf("lifecycle sidebar: %+v %v; want %d", page.Items, err, want)
		}
	}
	assertRetiredAfterRestart := func(lifecycle string) {
		t.Helper()
		app.desktopSessions.readSnapshots.close()
		app.stopHistoricalImports()
		app.closeSessionServices()
		app = newHistoricalLifecycleApp(t)
		assertRows(0)
		if _, err := app.ImportHistoricalSession(key); err == nil {
			t.Fatal("retired source was imported again")
		}
		current, err := app.workspaceRegistry().Load(t.Context())
		if err != nil || current.SessionStates[imported.Session.SessionID].Lifecycle != lifecycle || len(current.SourceMappings) != 1 {
			t.Fatalf("retired identity changed: %v", err)
		}
	}
	archive, err := app.ArchiveSessionTarget(SessionSelector{Source: &SessionSourceRef{Path: mapping.Path, SourceKey: key}})
	if err != nil || !archive.Committed {
		t.Fatalf("archive through old source: %+v %v", archive, err)
	}
	assertRetiredAfterRestart(workspacestate.Archived)
	trash, err := app.ListTrashEntries("", "", 50)
	if err != nil || len(trash.Items) != 1 || trash.Items[0].Ref == nil || *trash.Items[0].Ref != imported.Session {
		t.Fatalf("duplicate archive entries: %+v %v", trash, err)
	}
	if err := app.RestoreCanonicalSession(imported.Session); err != nil {
		t.Fatal(err)
	}
	assertRows(1)
	if err := app.ArchiveCanonicalSession(imported.Session); err != nil {
		t.Fatal(err)
	}
	if err := app.PurgeCanonicalSession(imported.Session); err != nil {
		t.Fatal(err)
	}
	assertRetiredAfterRestart(workspacestate.Deleted)
	app.desktopSessions.readSnapshots.close()
	if _, err := os.Stat(mapping.Path); !os.IsNotExist(err) {
		t.Fatalf("exclusive original survived purge: %v", err)
	}
}
