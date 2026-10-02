package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

func writeMigrationJSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func migrationSourceSnapshot(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[path] = body
	}
	return result
}

func assertMigrationSourceSnapshot(t *testing.T, before map[string][]byte) {
	t.Helper()
	for path, body := range before {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(body, after) {
			t.Fatalf("source changed: %s: %v", path, err)
		}
	}
}

func v5MigrationHistories(t *testing.T, app *App) map[string][]string {
	t.Helper()
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := map[string][]string{}
	for _, workspace := range state.Workspaces {
		for _, id := range workspace.SessionIDs {
			page, err := app.ReadSessionHistory(session.SessionRef{HostID: localDesktopHostID, SessionID: id}, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			var content []string
			for _, message := range page.Messages {
				content = append(content, message.Content)
			}
			result[id] = content
		}
	}
	return result
}

// Make a real, validated commit with the public writer, then encode it using
// the historical wire envelope. The retired format uses JSONL, not frames.
func writeV3MigrationFixture(t *testing.T, root, id, codec string, messages []provider.Message) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), id)
	writer, err := session.CreateStore(tmp, id)
	if err != nil {
		t.Fatal(err)
	}
	var events []session.Event
	for _, message := range messages {
		payload, _ := json.Marshal(map[string]any{"message": message})
		events = append(events, session.Event{Kind: "message/complete", Payload: payload})
	}
	if _, err := writer.Append(t.Context(), session.Batch{OperationID: "fixture", Events: events}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	commits, err := session.Replay(tmp, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, id)
	writeMigrationJSON(t, filepath.Join(dir, "manifest.json"), session.Manifest{SchemaVersion: 3, Codec: codec, SessionID: id, WriterGeneration: 1, CreatedAt: time.Now().UTC()})
	var log bytes.Buffer
	for _, commit := range commits {
		commit.SchemaVersion, commit.Codec = 3, codec
		if err := json.NewEncoder(&log).Encode(commit); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), log.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopV5UpgradeV1WithoutMetadata(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		for _, events := range []bool{false, true} {
			t.Run(scope+"/events="+map[bool]string{false: "no", true: "yes"}[events], func(t *testing.T) {
				isolateDesktopUserDirs(t)
				root := config.SessionDir()
				if scope == "project" {
					workspace := filepath.Join(t.TempDir(), "中文旧项目")
					root = desktopSessionDir(workspace)
					if err := saveProjectsFile(desktopProjectFile{Projects: []desktopProject{{Root: workspace}}}); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(root, "old.jsonl")
				writeMigrationJSON(t, path, provider.Message{Role: provider.RoleUser, Content: "checkpoint"})
				want := "checkpoint"
				if events {
					want = "newer-v1-event"
					writeMigrationJSON(t, store.SessionEventLog(path), struct {
						SchemaVersion int                `json:"schema_version"`
						Type          string             `json:"type"`
						Messages      []provider.Message `json:"messages"`
					}{1, "replace", []provider.Message{{Role: provider.RoleUser, Content: want}}})
				}
				// These are not independent transcripts even though they end in JSONL.
				for _, suffix := range []string{".turns.jsonl", ".guardian.jsonl", ".conflicts.jsonl"} {
					if err := os.WriteFile(filepath.Join(root, "old"+suffix), []byte("not a conversation\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before := migrationSourceSnapshot(t, legacyMigrationSourceFiles(path))
				app := NewApp()
				t.Cleanup(app.closeSessionServices)
				if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
					t.Fatal(err)
				}
				histories := v5MigrationHistories(t, app)
				if len(histories) != 1 {
					t.Fatalf("histories=%v", histories)
				}
				for id, history := range histories {
					if !reflect.DeepEqual(history, []string{want}) {
						t.Fatalf("history=%v", history)
					}
					appendMigrationTestMessage(t, app.desktopSessionService(""), session.SessionRef{HostID: localDesktopHostID, SessionID: id}, "continued")
				}
				app.closeSessionServices()
				app = NewApp()
				t.Cleanup(app.closeSessionServices)
				// A completed source must work without staging or history replay.
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
				for range 2 {
					if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				after := v5MigrationHistories(t, app)
				if len(after) != 1 {
					t.Fatalf("duplicate after continuation: %v", after)
				}
				for _, history := range after {
					if history[len(history)-1] != "continued" {
						t.Fatalf("lost continued history: %v", history)
					}
				}
				assertMigrationSourceSnapshot(t, before)
			})
		}
	}
}

func TestDesktopV5UpgradeAllV2HeadsAndOldReceipt(t *testing.T) {
	for _, previousMigration := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "previous-migrator"}[previousMigration], func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path := filepath.Join(config.SessionDir(), "branches.jsonl")
			legacy := agent.NewSession("system")
			legacy.Add(provider.Message{ID: "question", Role: provider.RoleUser, Content: "root question"})
			legacy.Add(provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "root answer"})
			if err := legacy.Save(path); err != nil {
				t.Fatal(err)
			}
			forkAt := legacy.Snapshot()[2].ID
			child, err := legacy.ForkHead(path, forkAt, agent.HeadKindFork, "child")
			if err != nil {
				t.Fatal(err)
			}
			legacy.Add(provider.Message{ID: "child-only", Role: provider.RoleUser, Content: "child only"})
			if err := legacy.Save(path); err != nil {
				t.Fatal(err)
			}
			heads, err := agent.ListSessionHeads(path)
			if err != nil {
				t.Fatal(err)
			}
			var rootHead string
			for _, head := range heads {
				if head.ID != child {
					rootHead = head.ID
				}
			}
			retired, err := legacy.ForkHead(path, forkAt, agent.HeadKindFork, "deleted")
			if err != nil {
				t.Fatal(err)
			}
			legacy.Add(provider.Message{ID: "deleted-work", Role: provider.RoleUser, Content: "retired branch"})
			if err := legacy.Save(path); err != nil {
				t.Fatal(err)
			}
			if err := legacy.SwitchHead(path, child); err != nil {
				t.Fatal(err)
			}
			if err := agent.RetireSessionHead(path, retired); err != nil {
				t.Fatal(err)
			}
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			var oldTarget string
			if previousMigration {
				if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{root: filepath.Dir(path), scope: "global"}, ""); err != nil {
					t.Fatal(err)
				}
				oldTarget = onlyMigrationRecord(t).TargetSessionID
				appendMigrationTestMessage(t, app.desktopSessionService(""), session.SessionRef{HostID: localDesktopHostID, SessionID: oldTarget}, "continued-before-upgrade")
				// Upgrade must recognize the receipt even if the old app selected
				// another head after that migration.
				if err := legacy.SwitchHead(path, rootHead); err != nil {
					t.Fatal(err)
				}
			}
			before := migrationSourceSnapshot(t, legacyMigrationSourceFiles(path))
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			histories := v5MigrationHistories(t, app)
			if len(histories) != 2 {
				t.Fatalf("expected both branches: %v", histories)
			}
			if previousMigration && histories[oldTarget][len(histories[oldTarget])-1] != "continued-before-upgrade" {
				t.Fatalf("old adopted target lost: %v", histories)
			}
			for id := range histories {
				appendMigrationTestMessage(t, app.desktopSessionService(""), session.SessionRef{HostID: localDesktopHostID, SessionID: id}, "v5-continued")
			}
			app.closeSessionServices()
			app = NewApp()
			t.Cleanup(app.closeSessionServices)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", filepath.Join(tmp, "missing"))
			for range 2 {
				if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if got := v5MigrationHistories(t, app); len(got) != 2 {
				t.Fatalf("duplicates: %v", got)
			}
			assertMigrationSourceSnapshot(t, before)
			t.Setenv("TMPDIR", tmp)
			if err := legacy.SwitchHead(path, child); err != nil {
				t.Fatal(err)
			}
			third, err := legacy.ForkHead(path, forkAt, agent.HeadKindFork, "third")
			if err != nil || third == "" {
				t.Fatal(err)
			}
			legacy.Add(provider.Message{ID: "third-work", Role: provider.RoleUser, Content: "third branch"})
			if err := legacy.Save(path); err != nil {
				t.Fatal(err)
			}
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := v5MigrationHistories(t, app); len(got) != 3 {
				t.Fatalf("new head missing or original duplicated: %v", got)
			}
			ledger, err := readDesktopMigrationLedger()
			if err != nil {
				t.Fatal(err)
			}
			primary := ledger.Records[desktopLegacyMigrationKey(path)].LegacyPrimaryHead
			if err := agent.RetireSessionHead(path, primary); err != nil {
				t.Fatal(err)
			}
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := v5MigrationHistories(t, app); len(got) != 3 {
				t.Fatalf("retiring primary changed other head identities: %v", got)
			}
		})
	}
}

func TestDesktopV5UpgradeV3Formats(t *testing.T) {
	for _, codec := range []string{session.PrototypeCodec, session.LegacyLinearCodec, session.FinalV31Codec} {
		t.Run(codec, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root, id := filepath.Join(filepath.Dir(config.SessionStoreDir()), "sessions-v3"), "old-v3"
			writeV3MigrationFixture(t, root, id, codec, []provider.Message{{ID: "user", Role: provider.RoleUser, Content: "v3 history"}})
			before := migrationSourceSnapshot(t, canonicalMigrationSourceFiles(root, id))
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			first := onlyMigrationRecord(t)
			histories := v5MigrationHistories(t, app)
			if !reflect.DeepEqual(histories[first.TargetSessionID], []string{"v3 history"}) {
				t.Fatalf("history=%v", histories)
			}
			appendMigrationTestMessage(t, app.desktopSessionService(""), session.SessionRef{HostID: localDesktopHostID, SessionID: first.TargetSessionID}, "v5 continued")
			app.closeSessionServices()
			app = NewApp()
			t.Cleanup(app.closeSessionServices)
			t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
			for range 2 {
				if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if record := onlyMigrationRecord(t); record.TargetSessionID != first.TargetSessionID || record.Attempts != first.Attempts {
				t.Fatalf("remigrated: %#v", record)
			}
			assertMigrationSourceSnapshot(t, before)
		})
	}
}

func TestDesktopV5UpgradePairedV3AndLegacy(t *testing.T) {
	for _, relation := range []string{"events-newer", "transcript-newer", "divergent"} {
		t.Run(relation, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path := filepath.Join(config.SessionDir(), "paired.jsonl")
			legacy := agent.NewSession("system")
			legacy.Add(provider.Message{ID: "user", Role: provider.RoleUser, Content: "common"})
			if err := legacy.Save(path); err != nil {
				t.Fatal(err)
			}
			messages := legacy.Snapshot()
			switch relation {
			case "events-newer":
				messages = append(messages, provider.Message{ID: "newer", Role: provider.RoleAssistant, Content: "event reply"})
			case "transcript-newer":
				legacy.Add(provider.Message{ID: "newer", Role: provider.RoleAssistant, Content: "transcript reply"})
				if err := legacy.Save(path); err != nil {
					t.Fatal(err)
				}
			case "divergent":
				messages[len(messages)-1].Content = "conflict"
			}
			writeV3MigrationFixture(t, config.SessionStoreDir(), agent.BranchID(path), session.LegacyLinearCodec, messages)
			before := migrationSourceSnapshot(t, desktopLegacySourceFiles(path, config.SessionStoreDir()))
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			err := app.migrateDesktopSessionsV5(t.Context())
			if relation == "divergent" {
				if err == nil {
					t.Fatal("divergent history silently selected")
				}
				if got := v5MigrationHistories(t, app); len(got) != 0 {
					t.Fatalf("published conflicting source: %v", got)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				histories := v5MigrationHistories(t, app)
				if len(histories) != 1 {
					t.Fatalf("paired history duplicated: %v", histories)
				}
				for _, history := range histories {
					if !strings.HasSuffix(history[len(history)-1], "reply") {
						t.Fatalf("new work lost: %v", history)
					}
				}
			}
			assertMigrationSourceSnapshot(t, before)
		})
	}
}

func TestDesktopV5UpgradeLegacyFailureRetryAndSibling(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionDir()
	writeMigrationJSON(t, filepath.Join(root, "healthy.jsonl"), provider.Message{Role: provider.RoleUser, Content: "healthy"})
	bad := filepath.Join(root, "broken.jsonl")
	if err := os.WriteFile(bad, []byte("{broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err == nil {
		t.Fatal("broken source silently ignored")
	}
	if got := v5MigrationHistories(t, app); len(got) != 1 {
		t.Fatalf("healthy sibling missing: %v", got)
	}
	writeMigrationJSON(t, bad, provider.Message{Role: provider.RoleUser, Content: "repaired"})
	app.closeSessionServices()
	app = NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs
	sort.Strings(ids)
	if len(ids) != 2 {
		t.Fatalf("retry failed: %v", ids)
	}
}

func TestDesktopV5UpgradePairedCanonicalKeepsPriorAdoption(t *testing.T) {
	for _, previousMigration := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "already-continued"}[previousMigration], func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path := filepath.Join(config.SessionDir(), "paired-canonical.jsonl")
			writeMigrationJSON(t, path, provider.Message{Role: provider.RoleUser, Content: "obsolete checkpoint"})
			root, id := config.SessionStoreDir(), agent.BranchID(path)
			coldV4MigrationFixture(t, root, id)
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			if previousMigration {
				if err := app.migrateCanonicalStore(t.Context(), desktopMigrationSource{root: root, scope: "global"}); err != nil {
					t.Fatal(err)
				}
				appendMigrationTestMessage(t, app.desktopSessionService(""), session.SessionRef{HostID: localDesktopHostID, SessionID: id}, "v5 continued")
			}
			before := migrationSourceSnapshot(t, desktopLegacySourceFiles(path, root))
			for range 2 {
				if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			history := v5MigrationHistories(t, app)
			if len(history) != 1 || history[id][0] != "恢复完整对话" {
				t.Fatalf("canonical authority lost or duplicated: %v", history)
			}
			if previousMigration && history[id][len(history[id])-1] != "v5 continued" {
				t.Fatalf("continued work lost: %v", history)
			}
			assertMigrationSourceSnapshot(t, before)
		})
	}
}

func TestDesktopV5UpgradeLegacyResumesWorkspacePublication(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := filepath.Join(config.SessionDir(), "interrupted.jsonl")
	writeMigrationJSON(t, path, provider.Message{Role: provider.RoleUser, Content: "durable history"})
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{root: filepath.Dir(path), scope: "global"}, "missing-workspace")
	if !errors.Is(err, workspacestate.ErrWorkspaceNotFound) {
		t.Fatalf("expected attach failure: %v", err)
	}
	first := onlyMigrationRecord(t)
	if first.Status != "failed" || first.TargetSessionID == "" {
		t.Fatalf("missing retry receipt: %#v", first)
	}
	app.closeSessionServices()
	app = NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	current := onlyMigrationRecord(t)
	if current.Status != "completed" || current.TargetSessionID != first.TargetSessionID {
		t.Fatalf("retry changed identity: %#v", current)
	}
	if got := v5MigrationHistories(t, app); len(got) != 1 {
		t.Fatalf("interrupted import duplicated: %v", got)
	}
}

func TestDesktopV5UpgradeProjectV3FailureRetry(t *testing.T) {
	isolateDesktopUserDirs(t)
	workspace := filepath.Join(t.TempDir(), "历史项目")
	if err := saveProjectsFile(desktopProjectFile{Projects: []desktopProject{{Root: workspace}}}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(config.ProjectSessionStoreDir(workspace)), "sessions-v3")
	writeV3MigrationFixture(t, root, "healthy", session.FinalV31Codec, []provider.Message{{ID: "user", Role: provider.RoleUser, Content: "healthy"}})
	writeV3MigrationFixture(t, root, "broken", session.PrototypeCodec, []provider.Message{{ID: "user", Role: provider.RoleUser, Content: "repaired"}})
	logPath := filepath.Join(root, "broken", "events.jsonl")
	original, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("{damaged}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err == nil {
		t.Fatal("damaged preview accepted")
	}
	if got := v5MigrationHistories(t, app); len(got) != 1 {
		t.Fatalf("healthy sibling missing: %v", got)
	}
	if err := os.WriteFile(logPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	app.closeSessionServices()
	app = NewApp()
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Workspaces[desktopWorkspaceID("project", workspace)].SessionIDs; len(got) != 2 {
		t.Fatalf("project v3 recovery=%v", got)
	}
}
