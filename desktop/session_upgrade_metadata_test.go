package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/topicstate"
)

func createUpgradeTopicFixture(t *testing.T, path, topicID, marker string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := topicstate.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(t.Context(), topicID, func(record *topicstate.Record) {
		record.Title = "preserved " + marker
		record.TitleSource = "manual"
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA wal_autocheckpoint=0`,
		`ALTER TABLE topics ADD COLUMN future_column TEXT NOT NULL DEFAULT ''`,
		`UPDATE topics SET future_column='future-` + marker + `' WHERE topic_id='` + topicID + `'`,
		`CREATE TABLE future_data(marker TEXT NOT NULL)`,
		`INSERT INTO future_data(marker) VALUES('` + marker + `')`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDesktopV1UpgradeBacksUpTopicWALAndRetriesWithoutDuplicateImport(t *testing.T) {
	isolateDesktopUserDirs(t)
	ctx := context.Background()
	projectRoot := filepath.Join(t.TempDir(), "project %20 # 中文")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	projects := []byte(`{"projects":[{"root":` + quotedJSON(projectRoot) + `,"topics":["project-topic"]}]}`)
	if err := os.MkdirAll(desktopConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktopConfigDir(), desktopProjectsFile), projects, 0o600); err != nil {
		t.Fatal(err)
	}

	registryPath := config.DesktopWorkspaceStatePath()
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	originalRegistry := []byte(`{"version":1,"generation":7,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","root":"/global","title":"Mine","visible":false,"sessionIds":["old"],"futureWorkspace":{"keep":42}}},"archivedSessionIds":["old"],"pendingCreates":{},"futureRoot":{"keep":true}}`)
	if err := os.WriteFile(registryPath, originalRegistry, 0o600); err != nil {
		t.Fatal(err)
	}

	historyDir := config.SessionDir()
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	historyPath := writeLegacySession(t, historyDir, "upgrade-history.jsonl", "preserve old history", time.Now())
	historyBefore := fileBytes(t, historyPath)

	globalPath := config.DesktopTopicStatePath("")
	projectPath := config.DesktopTopicStatePath(projectRoot)
	globalDB := createUpgradeTopicFixture(t, globalPath, "global-topic", "global")
	defer globalDB.Close()
	projectDB := createUpgradeTopicFixture(t, projectPath, "project-topic", "project")
	defer projectDB.Close()
	globalBefore := fileBytes(t, globalPath)
	projectBefore := fileBytes(t, projectPath)

	backupDir := filepath.Join(desktopConfigDir(), "desktop", "upgrade-backups")
	if err := os.MkdirAll(filepath.Dir(backupDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupDir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newDesktopWorkspaceStore()
	if err := store.RestoreSession(ctx, "old"); err == nil {
		t.Fatal("registry upgrade succeeded despite a blocked metadata backup directory")
	}
	if got := fileBytes(t, registryPath); !bytes.Equal(got, originalRegistry) {
		t.Fatal("failed upgrade published a new registry")
	}
	if got := fileBytes(t, globalPath); !bytes.Equal(got, globalBefore) {
		t.Fatal("failed upgrade changed the global topic database")
	}
	if got := fileBytes(t, projectPath); !bytes.Equal(got, projectBefore) {
		t.Fatal("failed upgrade changed the project topic database")
	}
	if got := fileBytes(t, historyPath); !bytes.Equal(got, historyBefore) {
		t.Fatal("failed upgrade changed old session history")
	}

	if err := os.Remove(backupDir); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreSession(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	state, err := newDesktopWorkspaceStore().Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != workspacestate.SchemaVersion || state.SessionStates["old"].Lifecycle != workspacestate.Active {
		t.Fatalf("upgraded registry = %+v", state)
	}
	count := 0
	for _, id := range state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs {
		if id == "old" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("old session membership count = %d, want 1", count)
	}
	registryBody := string(fileBytes(t, registryPath))
	for _, field := range []string{`"futureRoot"`, `"futureWorkspace"`} {
		if !strings.Contains(registryBody, field) {
			t.Fatalf("registry lost unknown field %s: %s", field, registryBody)
		}
	}
	if got := fileBytes(t, globalPath); !bytes.Equal(got, globalBefore) {
		t.Fatal("successful upgrade changed the global topic database")
	}
	if got := fileBytes(t, projectPath); !bytes.Equal(got, projectBefore) {
		t.Fatal("successful upgrade changed the project topic database")
	}
	if got := fileBytes(t, historyPath); !bytes.Equal(got, historyBefore) {
		t.Fatal("successful upgrade changed old session history")
	}

	backups, err := filepath.Glob(filepath.Join(backupDir, "topics-*.sqlite"))
	if err != nil || len(backups) != 2 {
		t.Fatalf("topic backups = %v, %v; want two", backups, err)
	}
	markers := map[string]bool{}
	for _, backup := range backups {
		db, err := sql.Open("sqlite", backup)
		if err != nil {
			t.Fatal(err)
		}
		var marker, title, future string
		if err := db.QueryRowContext(ctx, `SELECT marker FROM future_data`).Scan(&marker); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		topicID := marker + "-topic"
		if err := db.QueryRowContext(ctx, `SELECT title,future_column FROM topics WHERE topic_id=?`, topicID).Scan(&title, &future); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if title != "preserved "+marker || future != "future-"+marker {
			t.Fatalf("backup %s lost data: marker=%q title=%q future=%q", backup, marker, title, future)
		}
		markers[marker] = true
	}
	if !markers["global"] || !markers["project"] {
		t.Fatalf("backup markers = %v", markers)
	}
}

func quotedJSON(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}
