package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// Re-encode a real conversion in the retired JSONL codec, retaining Source and
// the immutable legacy snapshot exactly as old v2 -> v3 migration did.
func encodeMigrationFixtureAsV3(t *testing.T, dir string) {
	t.Helper()
	commits, err := session.Replay(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readDesktopMigrationManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion, manifest.Codec, manifest.StorageRevision, manifest.ContentRoot = 3, session.FinalV31Codec, 0, ""
	writeMigrationJSON(t, filepath.Join(dir, "manifest.json"), manifest)
	var log bytes.Buffer
	for _, commit := range commits {
		commit.SchemaVersion, commit.Codec = 3, session.FinalV31Codec
		if err := json.NewEncoder(&log).Encode(commit); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), log.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "events.frames")); err != nil {
		t.Fatal(err)
	}
}

func convertedV3Fixture(t *testing.T, path, root, head string, extra ...string) string {
	t.Helper()
	converted, err := session.MigrateLegacyHead(t.Context(), path, root, head)
	if err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("fixture", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range extra {
		appendMigrationTestMessage(t, service, session.SessionRef{HostID: "fixture", SessionID: converted.TargetID}, id)
	}
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	encodeMigrationFixtureAsV3(t, converted.TargetDir)
	return converted.TargetID
}

func migrationSingleDAGFixture(t *testing.T) (string, *agent.Session, string) {
	t.Helper()
	path := filepath.Join(config.SessionDir(), "original-v2.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "question", Role: provider.RoleUser, Content: "original question"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	heads, err := agent.ListSessionHeads(path)
	if err != nil || len(heads) != 1 {
		t.Fatalf("DAG fixture: %v %v", heads, err)
	}
	return path, legacy, heads[0].ID
}

func assertLineageRestart(t *testing.T, app *App, count int) {
	t.Helper()
	histories := v5MigrationHistories(t, app)
	if len(histories) != count {
		t.Fatalf("expected %d independent histories: %v", count, histories)
	}
	for id := range histories {
		appendMigrationTestMessage(t, app.desktopSessionService(""), session.SessionRef{HostID: localDesktopHostID, SessionID: id}, "continued-in-v5")
	}
	app.closeSessionServices()
	ledgerBefore, err := os.ReadFile(desktopMigrationLedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewApp()
	t.Cleanup(restarted.closeSessionServices)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	for range 2 {
		if err := restarted.migrateDesktopSessionsV5(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ledgerAfter, err := os.ReadFile(desktopMigrationLedgerPath())
	if err != nil || !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatalf("unchanged lineage rewrote ledger: %v", err)
	}
	after := v5MigrationHistories(t, restarted)
	if len(after) != count {
		t.Fatalf("restart duplicated histories: %v", after)
	}
	for id := range histories {
		if got := after[id]; len(got) == 0 || got[len(got)-1] != "continued-in-v5" {
			t.Fatalf("continued target lost: %s %v", id, got)
		}
	}
	// Completed aliases must not resurrect a deliberately deleted v5 target.
	for id := range histories {
		if err := restarted.desktopSessionService("").Delete(t.Context(), session.SessionRef{HostID: localDesktopHostID, SessionID: id}); err != nil {
			t.Fatal(err)
		}
		if err := restarted.migrateDesktopSessionsV5(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(restarted.desktopSessions.root, id)); !os.IsNotExist(err) {
			t.Fatalf("deleted target resurrected: %v", err)
		}
		break
	}
}

func TestDesktopV5LineageV2ConvertedToV3(t *testing.T) {
	for _, explicitHead := range []bool{false, true} {
		for _, scenario := range []string{"equal", "v3-newer", "v2-newer", "diverged", "v2-already-in-v5", "v3-already-in-v5"} {
			t.Run(scenario+map[bool]string{false: "/selected", true: "/explicit"}[explicitHead], func(t *testing.T) {
				isolateDesktopUserDirs(t)
				path, legacy, head := migrationSingleDAGFixture(t)
				selected := ""
				if explicitHead {
					selected = head
				}
				root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
				var extra []string
				if scenario == "v3-newer" || scenario == "diverged" || scenario == "v3-already-in-v5" {
					extra = []string{"continued-in-v3"}
				}
				id := convertedV3Fixture(t, path, root, selected, extra...)
				if scenario == "v2-newer" || scenario == "diverged" {
					legacy.Add(provider.Message{ID: "v2-added", Role: provider.RoleUser, Content: "continued-in-v2"})
					if err := legacy.Save(path); err != nil {
						t.Fatal(err)
					}
				}
				before := migrationSourceSnapshot(t, append(legacyMigrationSourceFiles(path), canonicalMigrationSourceFiles(root, id)...))
				app := NewApp()
				t.Cleanup(app.closeSessionServices)
				if scenario == "v2-already-in-v5" {
					if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{root: filepath.Dir(path), scope: "global"}, ""); err != nil {
						t.Fatal(err)
					}
					old := onlyMigrationRecord(t)
					appendMigrationTestMessage(t, app.desktopSessionService(""), session.SessionRef{HostID: localDesktopHostID, SessionID: old.TargetSessionID}, "pre-existing-v5-work")
				}
				if scenario == "v3-already-in-v5" {
					if err := app.migratePreviewSession(t.Context(), desktopMigrationSource{root: root, scope: "global"}, id); err != nil {
						t.Fatal(err)
					}
					old := onlyMigrationRecord(t)
					appendMigrationTestMessage(t, app.desktopSessionService(""), session.SessionRef{HostID: localDesktopHostID, SessionID: old.TargetSessionID}, "pre-existing-v5-work")
				}
				if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
					t.Fatal(err)
				}
				count := 1
				if scenario == "diverged" {
					count = 2
				}
				histories := v5MigrationHistories(t, app)
				if len(histories) != count {
					t.Fatalf("converted source duplicated: %v", histories)
				}
				want := "original question"
				if scenario == "v3-newer" {
					want = "continued-in-v3"
				}
				if scenario == "v2-newer" {
					want = "continued-in-v2"
				}
				if scenario == "v3-already-in-v5" || scenario == "v2-already-in-v5" {
					want = "pre-existing-v5-work"
				}
				if scenario != "diverged" {
					for _, history := range histories {
						if history[len(history)-1] != want {
							t.Fatalf("newer history lost: %v", history)
						}
					}
				}
				assertLineageRestart(t, app, count)
				assertMigrationSourceSnapshot(t, before)
			})
		}
	}
}

func TestDesktopV5LineageResumesAfterTargetPublication(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, _, head := migrationSingleDAGFixture(t)
	root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
	id := convertedV3Fixture(t, path, root, head, "converted continuation")
	converted := desktopMigrationConversion{Root: root, SessionID: id, HeadID: head, Codec: session.FinalV31Codec, Depth: 1, LegacyDir: filepath.Join(root, id, "legacy")}
	source := desktopMigrationSource{root: filepath.Dir(path), scope: "global", headConversions: []desktopMigrationConversion{converted}, conversions: map[string][]desktopMigrationConversion{canonicalRuntimeRoot(path): {converted}}}
	cp, err := newDesktopMigrationCheckpoint(source, desktopLegacyMigrationKey(path), desktopLegacyMigrationFiles(path, source))
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	app.desktopSessions.beforeMigrationRegistryCommit = func() error { return fmt.Errorf("injected registry failure") }
	if err := app.migrateConversionLineage(t.Context(), path, head, source, &cp, workspace); err == nil {
		t.Fatal("expected interrupted workspace publication")
	}
	infos, err := listAllCanonicalSessionInfo(t.Context(), app.desktopSessionService("").Query())
	if err != nil || len(infos) != 1 {
		t.Fatalf("expected one durable published target: %v %v", infos, err)
	}
	var first string
	for id := range infos {
		first = id
	}
	app.closeSessionServices()
	app = NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := v5MigrationHistories(t, app)
	if len(got) != 1 || len(got[first]) == 0 {
		t.Fatalf("retry changed or duplicated target: %v", got)
	}
	assertLineageRestart(t, app, 1)
}

func TestDesktopV5LineageConvertedHeadDoesNotHideOtherHeads(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, originalHead := migrationSingleDAGFixture(t)
	child, err := legacy.ForkHead(path, legacy.Snapshot()[1].ID, agent.HeadKindFork, "other")
	if err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{ID: "child", Role: provider.RoleUser, Content: "child history"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
	convertedV3Fixture(t, path, root, "", "v3 child continuation")
	// The manifest's omitted head refers to child at conversion time.
	if err := legacy.SwitchHead(path, originalHead); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	histories := v5MigrationHistories(t, app)
	if len(histories) != 2 {
		t.Fatalf("converted %s hid another head or duplicated itself: %v", child, histories)
	}
	assertLineageRestart(t, app, 2)
}

func TestDesktopV5LineageMultipleConversions(t *testing.T) {
	for _, diverged := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-history", true: "independent-continuations"}[diverged], func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path, _, head := migrationSingleDAGFixture(t)
			v3root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
			convertedV3Fixture(t, path, v3root, head, "first conversion work")
			extra := "first conversion work"
			if diverged {
				extra = "independent second conversion"
			}
			convertedV3Fixture(t, path, config.SessionStoreDir(), head, extra)
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := 1
			if diverged {
				want = 2
			}
			assertLineageRestart(t, app, want)
		})
	}
}

func TestDesktopV5LineageMultipleGenerations(t *testing.T) {
	for _, legacyPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "stored-only", true: "v2-v3-v4"}[legacyPresent], func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path, _, _ := migrationSingleDAGFixture(t)
			v3root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
			id := convertedV3Fixture(t, path, v3root, "", "v3 continuation")
			v4, err := session.NewService("fixture", session.NewFilesystemPersistence(config.SessionStoreDir()))
			if err != nil {
				t.Fatal(err)
			}
			runtime, _, err := v4.ContinuePrototype(t.Context(), filepath.Join(v3root, id))
			if err != nil {
				t.Fatal(err)
			}
			appendMigrationTestMessage(t, v4, runtime.Ref(), "v4 continuation")
			if err := v4.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !legacyPresent {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, history := range v5MigrationHistories(t, app) {
				if history[len(history)-1] != "v4 continuation" {
					t.Fatalf("latest generation lost: %v", history)
				}
			}
			ledger, err := readDesktopMigrationLedger()
			if err != nil {
				t.Fatal(err)
			}
			ancestor := ledger.Records[desktopCanonicalMigrationKey(v3root, id)]
			descendant := ledger.Records[desktopCanonicalMigrationKey(config.SessionStoreDir(), runtime.Ref().SessionID)]
			if ancestor.ContentDigest == "" || ancestor.ContentDigest == descendant.ContentDigest {
				t.Fatal("ancestor digest was replaced by the continued descendant during staging")
			}
			assertLineageRestart(t, app, 1)
		})
	}
}

func TestDesktopV5LineageNativeV3AndUnrelatedEqualSessions(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
	messages := []provider.Message{{ID: "same", Role: provider.RoleUser, Content: "same content"}}
	writeV3MigrationFixture(t, root, "native", session.FinalV31Codec, messages)
	writeV3MigrationFixture(t, root, "unrelated", session.FinalV31Codec, messages)
	v4, err := session.NewService("fixture", session.NewFilesystemPersistence(config.SessionStoreDir()))
	if err != nil {
		t.Fatal(err)
	}
	runtime, _, err := v4.ContinuePrototype(t.Context(), filepath.Join(root, "native"))
	if err != nil {
		t.Fatal(err)
	}
	appendMigrationTestMessage(t, v4, runtime.Ref(), "native v4 work")
	if err := v4.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLineageRestart(t, app, 2)
}

func TestDesktopV5LineageDeletedNativeV3WithMultipleConversions(t *testing.T) {
	for _, scenario := range []string{"equal", "prefix", "diverged"} {
		t.Run(scenario, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
			writeV3MigrationFixture(t, root, "native", session.FinalV31Codec, []provider.Message{{ID: "original", Role: provider.RoleUser, Content: "original"}})
			for index, targetRoot := range []string{config.SessionStoreDir(), config.ProjectSessionStoreDir(globalWorkspaceRoot())} {
				v4, err := session.NewService("fixture", session.NewFilesystemPersistence(targetRoot))
				if err != nil {
					t.Fatal(err)
				}
				runtime, _, err := v4.ContinuePrototype(t.Context(), filepath.Join(root, "native"))
				if err != nil {
					t.Fatal(err)
				}
				work := "shared continuation"
				if index == 1 && scenario == "diverged" {
					work = "independent continuation"
				}
				appendMigrationTestMessage(t, v4, runtime.Ref(), work)
				if index == 1 && scenario == "prefix" {
					appendMigrationTestMessage(t, v4, runtime.Ref(), "later work")
				}
				if err := v4.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.RemoveAll(filepath.Join(root, "native")); err != nil {
				t.Fatal(err)
			}
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := 1
			if scenario == "diverged" {
				want = 2
			}
			assertLineageRestart(t, app, want)
		})
	}
}

func TestDesktopV5LineageIncludesUnstampedPairedCanonical(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, head := migrationSingleDAGFixture(t)
	root := config.SessionStoreDir()
	convertedV3Fixture(t, path, filepath.Join(filepath.Dir(root), "sessions-v3"), head, "converted work")
	canonical, err := session.NewService("fixture", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := canonical.Create(t.Context(), session.CreateOptions{SessionID: agent.BranchID(path)})
	if err != nil {
		t.Fatal(err)
	}
	for index, message := range legacy.Snapshot() {
		payload, _ := json.Marshal(map[string]any{"message": message})
		if _, err := runtime.Session().AppendBatch(t.Context(), fmt.Sprintf("seed-%d", index), []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
			t.Fatal(err)
		}
	}
	appendMigrationTestMessage(t, canonical, runtime.Ref(), "converted work")
	appendMigrationTestMessage(t, canonical, runtime.Ref(), "paired canonical work")
	if err := canonical.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, history := range v5MigrationHistories(t, app) {
		if history[len(history)-1] != "paired canonical work" {
			t.Fatalf("paired canonical history omitted: %v", history)
		}
	}
	assertLineageRestart(t, app, 1)
}
