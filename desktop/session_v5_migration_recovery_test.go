package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestDesktopV5UpgradeExcludesAutomaticRecoveryCopies(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	for _, fixture := range []struct {
		name string
		meta agent.BranchMeta
		want bool
	}{
		{"ordinary", agent.BranchMeta{}, true},
		{"user-fork", agent.BranchMeta{ParentID: "ordinary"}, true},
		{"my-recovery-notes", agent.BranchMeta{}, true},
		{"marked-copy", agent.BranchMeta{Recovered: true, ParentID: "ordinary"}, false},
		{"version-copy", agent.BranchMeta{VersionKind: agent.VersionRecovery}, false},
		{"digest-copy", agent.BranchMeta{RecoveryDigest: "historical-marker"}, false},
		{"ordinary-recovery-0123456789abcdef", agent.BranchMeta{}, false},
		{"ordinary-recovery-0123456789abcdef-recovery-fedcba9876543210", agent.BranchMeta{}, false},
	} {
		path := filepath.Join(dir, fixture.name+".jsonl")
		legacy := agent.NewSession("system")
		legacy.Add(provider.Message{ID: fixture.name, Role: provider.RoleUser, Content: fixture.name})
		if err := legacy.Save(path); err != nil {
			t.Fatal(err)
		}
		writeMigrationJSON(t, agent.BranchMetaPath(path), fixture.meta)
		if fixture.name == "ordinary-recovery-0123456789abcdef" {
			if err := os.Remove(agent.BranchMetaPath(path)); err != nil {
				t.Fatal(err)
			}
		}
		if got := desktopMigrationAutomaticRecovery(path); got == fixture.want {
			t.Fatalf("unexpected recovery classification: %s = %v", fixture.name, got)
		}
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, history := range v5MigrationHistories(t, app) {
		last := history[len(history)-1]
		if last != "ordinary" && last != "user-fork" && last != "my-recovery-notes" {
			t.Fatalf("recovery artifact appeared as a conversation: %v", history)
		}
	}
	assertLineageRestart(t, app, 3)
}

func TestDesktopV5UpgradeExcludesConvertedRecoveryCopies(t *testing.T) {
	for _, removed := range []bool{false, true} {
		name := "original-present"
		if removed {
			name = "original-removed"
		}
		t.Run(name, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path, _, _ := migrationSingleDAGFixture(t)
			copyPath := filepath.Join(filepath.Dir(path), "original-v2-recovery-0123456789abcdef.jsonl")
			copySession := agent.NewSession("system")
			copySession.Add(provider.Message{ID: "copy-only", Role: provider.RoleUser, Content: "automatic conflict data"})
			if err := copySession.Save(copyPath); err != nil {
				t.Fatal(err)
			}
			v3root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
			id := convertedV3Fixture(t, copyPath, v3root, "")
			v4, err := session.NewService("fixture", session.NewFilesystemPersistence(config.SessionStoreDir()))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := v4.ContinuePrototype(t.Context(), filepath.Join(v3root, id)); err != nil {
				t.Fatal(err)
			}
			if err := v4.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if removed {
				for _, file := range legacyMigrationSourceFiles(copyPath) {
					if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
				// Only the descendant's archived manifest still knows the origin.
				if err := os.RemoveAll(filepath.Join(v3root, id)); err != nil {
					t.Fatal(err)
				}
			}
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertLineageRestart(t, app, 1)
		})
	}
}

func TestDesktopV5UpgradeExcludesRecoveryStoreWithoutTranscript(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	for _, id := range []string{"ordinary", "only-meta-copy", "ordinary-recovery-0123456789abcdef"} {
		service, err := session.NewService("fixture", session.NewFilesystemPersistence(root))
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: id})
		if err != nil {
			t.Fatal(err)
		}
		appendMigrationTestMessage(t, service, runtime.Ref(), id)
		if err := service.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	writeMigrationJSON(t, agent.BranchMetaPath(filepath.Join(config.SessionDir(), "only-meta-copy.jsonl")), agent.BranchMeta{Recovered: true})
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLineageRestart(t, app, 1)
}

func TestDesktopV5UpgradeExcludesArchivedRecoveryMetadata(t *testing.T) {
	isolateDesktopUserDirs(t)
	migrationSingleDAGFixture(t)
	path := filepath.Join(config.SessionDir(), "metadata-marked-copy.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "automatic", Role: provider.RoleUser, Content: "conflict copy"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	writeMigrationJSON(t, agent.BranchMetaPath(path), agent.BranchMeta{Recovered: true})
	root := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3")
	id := convertedV3Fixture(t, path, root, "")
	for _, file := range legacyMigrationSourceFiles(path) {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	archived := filepath.Join(root, id, "legacy", filepath.Base(path))
	before := migrationSourceSnapshot(t, append(canonicalMigrationSourceFiles(root, id), legacyMigrationSourceFiles(archived)...))
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLineageRestart(t, app, 1)
	assertMigrationSourceSnapshot(t, before)
}
