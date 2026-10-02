package main

import (
	"errors"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/session"
)

func TestHistoricalReceiptResolutionPreservesLiveOwners(t *testing.T) {
	for _, retired := range []int{0, 1, 2} {
		t.Run(string(rune('0'+retired)), func(t *testing.T) {
			app, selector, _ := historicalArchiveFixture(t)
			first, err := app.ImportHistoricalSession(selector.Source.SourceKey)
			if err != nil {
				t.Fatal(err)
			}
			second, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{first.Session.SessionID, second.Ref().SessionID}
			if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspacestate.GlobalWorkspaceID, ids[1], ""); err != nil {
				t.Fatal(err)
			}
			rewriteHistoricalRegistry(t, app, func(state *workspacestate.State) { state.SourceMappings = map[string]workspacestate.SourceMapping{} })
			for i := range retired {
				if err := app.workspaceRegistry().ArchiveSession(t.Context(), ids[i]); err != nil {
					t.Fatal(err)
				}
				state, err := app.workspaceRegistry().Load(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if err := app.workspaceRegistry().BeginPurge(t.Context(), ids[i], state.Generation); err != nil {
					t.Fatal(err)
				}
			}
			receipts := []desktopMigrationReceipt{{TargetSessionID: ids[0], ContentDigest: "same"}, {TargetSessionID: ids[1], ContentDigest: "same"}}
			target, err := app.selectCanonicalConversionTarget(t.Context(), desktopMigrationSource{scope: "global"}, selector.Source.Path, "same", receipts)
			if retired == 0 {
				if !errors.Is(err, workspacestate.ErrMutationConflict) {
					t.Fatalf("picked competing live owner %s: %v", target, err)
				}
			} else if err != nil || target == "" || retired == 1 && target != ids[1] {
				t.Fatalf("unique owner not recovered: %s %v", target, err)
			}
		})
	}
}

func TestHistoricalGlobalSourceIgnoresRemovedProjectMetadata(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, _, head := migrationSingleDAGFixture(t)
	missing := filepath.Join(t.TempDir(), "removed-project")
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{Scope: "project", WorkspaceRoot: missing}); err != nil {
		t.Fatal(err)
	}
	app := newHistoricalLifecycleApp(t)
	for _, selectedHead := range []string{"", head} {
		selector := SessionSelector{Source: &SessionSourceRef{Path: path, HeadID: selectedHead}}
		target, err := app.resolveSourceSessionTarget(selector, false)
		if err != nil || target.Scope != "global" || target.Source == nil {
			t.Fatalf("stale project blocks global history: %+v %v", target, err)
		}
	}
	local := recoverHistoricalRuntimeOwner(SessionTarget{Scope: "project", WorkspaceRoot: missing, Source: &SessionSourceRef{Path: filepath.Join(missing, ".reasonix", "sessions", "history.jsonl")}})
	if local.Scope != "project" {
		t.Fatal("project source silently moved to another workspace")
	}
}
