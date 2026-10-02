package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// An old build may adopt only the JSONL receipt, leaving its converted store
// unregistered. A later import must not bypass the destination tombstone.
func TestUnmappedHistoricalConversionDoesNotRevivePurgedLegacyTarget(t *testing.T) {
	for _, continued := range []bool{false, true} {
		name := "same-content"
		if continued {
			name = "continued-conversion"
		}
		t.Run(name, func(t *testing.T) { checkUnmappedPurgedConversion(t, continued) })
	}
	t.Run("divergent-tool-history", func(t *testing.T) {
		checkUnmappedPurgedConversion(t, true, func(old *session.Service, ref session.SessionRef) {
			binding, err := old.Open(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			messages := []provider.Message{
				{Role: provider.RoleUser, Content: "independent branch", RawContent: "complete original user input"},
				{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call", Name: "fixture_tool", Arguments: "{}"}}},
				{Role: provider.RoleTool, ToolCallID: "call", Content: "complete tool result"},
			}
			payload, err := json.Marshal(map[string]any{"messages": messages})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := binding.Runtime().Session().AppendBatch(t.Context(), "divergence", []session.Event{{Kind: "history/replace", Payload: payload}}); err != nil {
				t.Fatal(err)
			}
			if err := binding.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := old.Close(t.Context(), ref); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("omitted-conversion-head", func(t *testing.T) {
		checkUnmappedPurgedConversion(t, false, func(old *session.Service, ref session.SessionRef) {
			path := filepath.Join(config.SessionStoreDir(), ref.SessionID, "manifest.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest session.Manifest
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Source.LegacyHeadID = ""
			body, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
		})
	})
}

func checkUnmappedPurgedConversion(t *testing.T, continued bool, changes ...func(*session.Service, session.SessionRef)) {
	t.Helper()
	isolateDesktopUserDirs(t)
	path, _, head := migrationSingleDAGFixture(t)
	root := config.SessionStoreDir()
	converted, err := session.MigrateLegacyHead(t.Context(), path, root, head)
	if err != nil {
		t.Fatal(err)
	}
	if continued || len(changes) != 0 {
		old, err := session.NewService("fixture", session.NewFilesystemPersistence(root))
		if err != nil {
			t.Fatal(err)
		}
		ref := session.SessionRef{HostID: "fixture", SessionID: converted.TargetID}
		if len(changes) != 0 {
			changes[0](old, ref)
		} else {
			appendMigrationTestMessage(t, old, ref, "independent later work")
		}
		if err := old.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	app := newHistoricalLifecycleApp(t)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{scope: "global", headID: head}, workspace); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping := state.SourceMappings[desktopSourceKey(path, head)]
	if mapping.SessionID != converted.TargetID {
		t.Fatalf("fixture must share the migrated identity: %s != %s", mapping.SessionID, converted.TargetID)
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	if err := app.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if err := app.PurgeCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	app.stopHistoricalImports()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	before := migrationSourceSnapshot(t, canonicalMigrationSourceFiles(root, converted.TargetID))
	key := historicalLifecycleID(t, app, converted.TargetID)
	selector := SessionSelector{Source: &SessionSourceRef{
		Path: filepath.Join(root, converted.TargetID), SourceKey: key,
	}}
	result, err := app.ArchiveSessionTarget(selector)
	if err != nil || !result.Committed {
		t.Fatalf("archive: %+v, %v", result, err)
	}
	state, loadErr := app.workspaceRegistry().Load(t.Context())
	if loadErr != nil || state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatalf("purged adoption changed: %v", loadErr)
	}
	canonicalMapping, exists, err := state.ResolveSource(key)
	if err != nil || !exists {
		t.Fatalf("missing canonical receipt: %v", err)
	}
	if continued {
		if canonicalMapping.SessionID == ref.SessionID || state.SessionStates[canonicalMapping.SessionID].Lifecycle != workspacestate.Archived || result.Outcome != "archived_copy" {
			t.Fatalf("later work was not independently archived: %+v, %+v", canonicalMapping, result)
		}
		digest, err := canonicalMigrationDigest(t.Context(), app.desktopSessionService("").Query(), session.SessionRef{HostID: localDesktopHostID, SessionID: canonicalMapping.SessionID})
		old, openErr := session.NewService("source", session.NewFilesystemPersistence(root))
		if openErr != nil {
			t.Fatal(openErr)
		}
		expected, readErr := canonicalMigrationDigest(t.Context(), old.Query(), session.SessionRef{HostID: "source", SessionID: converted.TargetID})
		_ = old.Shutdown(t.Context())
		if err != nil || readErr != nil || digest != expected {
			t.Fatalf("full history changed: %v, %v", err, readErr)
		}
	} else if canonicalMapping.SessionID != ref.SessionID || result.Outcome != "already_removed" {
		t.Fatalf("identical retired copy resurrected: %+v, %+v", canonicalMapping, result)
	}
	beforeStates := len(state.SessionStates)
	app.stopHistoricalImports()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	repeated, err := app.ArchiveSessionTarget(selector)
	if err != nil || !repeated.Committed || repeated.TargetKey != result.TargetKey {
		t.Fatalf("restart retry: %+v, %v", repeated, err)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.SessionStates) != beforeStates {
		t.Fatalf("duplicate target after retry: %v", err)
	}
	if continued {
		copyRef := session.SessionRef{HostID: localDesktopHostID, SessionID: canonicalMapping.SessionID}
		if err := app.RestoreCanonicalSession(copyRef); err != nil {
			t.Fatal(err)
		}
		if err := app.ArchiveCanonicalSession(copyRef); err != nil {
			t.Fatal(err)
		}
	}
	ledger, loadErr := readDesktopMigrationLedger()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	record := ledger.Records[desktopCanonicalMigrationKey(root, converted.TargetID)]
	adopted := ledger.Records[desktopLegacyHeadKey(path, head)]
	if record.ContentDigest == "" || (record.ContentDigest != adopted.ContentDigest) != continued {
		t.Fatal("fixture did not preserve the distinction between an identical copy and later work")
	}
	t.Logf("conversion ledger after archive: status=%s target=%s attempts=%d", record.Status, record.TargetSessionID, record.Attempts)
	assertMigrationSourceSnapshot(t, before)
}
