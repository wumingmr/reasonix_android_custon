// Package upgradefixture creates and verifies disposable legacy data for native
// packaged-app acceptance and the Desktop migration regression.
package upgradefixture

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/sqliteuri"
	"reasonix/internal/topicstate"
)

const (
	fixtureSessionID = "windows-upgrade-fixture"
	fixtureTopicID   = "windows-upgrade-topic"
	fixtureTitle     = "Upgrade fixture topic"
	fixtureQuestion  = "Please restore my earlier conversation."
	fixtureText      = "Restored assistant body: upgrade-history-7c82 中文 %20 #"
)

type fixtureReport struct {
	Home           string `json:"home"`
	RegistryPath   string `json:"registryPath"`
	RegistrySHA256 string `json:"registrySha256"`
	TopicID        string `json:"topicId"`
	VisibleText    string `json:"visibleText"`
	ProjectRoot    string `json:"projectRoot"`
	LegacyPath     string `json:"legacyPath"`
}

type legacyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Run requires an isolated, disposable home. It never starts the application.
func Run(mode, home, reportPath, phase string) error {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(reportPath) == "" {
		return errors.New("--home and --report are required")
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	for key, value := range map[string]string{
		"REASONIX_HOME": absHome, "REASONIX_STATE_HOME": absHome, "REASONIX_CACHE_HOME": filepath.Join(absHome, "cache"),
	} {
		previous, existed := os.LookupEnv(key)
		if err := os.Setenv(key, value); err != nil {
			return err
		}
		defer func() {
			if existed {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		}()
	}
	switch mode {
	case "create":
		return createFixture(context.Background(), absHome, reportPath)
	case "verify":
		return verifyFixture(context.Background(), reportPath, phase)
	default:
		return errors.New("--mode must be create or verify")
	}
}

func createFixture(ctx context.Context, home, reportPath string) error {
	if _, err := os.Stat(home); err == nil {
		entries, readErr := os.ReadDir(home)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return errors.New("fixture home must be empty")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	// Model an existing installation without credentials or a live provider.
	// Otherwise first-run onboarding opens settings instead of the restored tab.
	cfg := config.Default()
	cfg.DefaultModel = "upgrade-fixture/offline"
	cfg.Desktop.ProviderAccess = []string{"upgrade-fixture"}
	cfg.Providers = []config.ProviderEntry{{
		Name: "upgrade-fixture", Kind: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "offline",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		return err
	}
	legacyPath := filepath.Join(config.SessionDir(), fixtureSessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		return err
	}
	legacy, err := encodeLegacyHistory(
		legacyMessage{Role: "user", Content: fixtureQuestion},
		legacyMessage{Role: "assistant", Content: fixtureText},
	)
	if err != nil {
		return err
	}
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		return err
	}
	if err := agent.SaveBranchMetaPreserveUpdated(legacyPath, agent.BranchMeta{Scope: "global", TopicID: fixtureTopicID, TopicTitle: fixtureTitle}); err != nil {
		return err
	}

	projectRoot := filepath.Join(home, "project # %20 中文")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		return err
	}
	projectsBody, err := json.Marshal(map[string]any{"projects": []map[string]any{{"root": projectRoot, "topics": []string{"project-topic"}}}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(config.ReasonixHomeDir(), "desktop-projects.json"), projectsBody, 0o600); err != nil {
		return err
	}
	if err := createTopicDatabase(ctx, config.DesktopTopicStatePath(""), fixtureTopicID, "global"); err != nil {
		return err
	}
	if err := createTopicDatabase(ctx, config.DesktopTopicStatePath(projectRoot), "project-topic", "project"); err != nil {
		return err
	}

	registry, err := json.Marshal(map[string]any{
		"version":      1,
		"generation":   7,
		"workspaceIds": []string{"global"},
		"workspaces": map[string]any{
			"global": map[string]any{
				"id":              "global",
				"root":            filepath.Join(config.ReasonixHomeDir(), "global-workspace"),
				"title":           "Global",
				"visible":         true,
				"sessionIds":      []string{},
				"futureWorkspace": map[string]any{"preserve": 42},
			},
		},
		"archivedSessionIds": []string{},
		"pendingCreates":     map[string]any{},
		"futureRoot":         map[string]any{"preserve": true},
	})
	if err != nil {
		return err
	}
	registryPath := config.DesktopWorkspaceStatePath()
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		return err
	}
	tabs := map[string]any{
		"tabs":      []map[string]any{{"id": "upgrade-tab", "scope": "global", "workspaceId": "global", "topicId": fixtureTopicID, "sessionPath": legacyPath}},
		"activeTab": "upgrade-tab", "tabOrder": []string{"upgrade-tab"},
	}
	tabsBody, err := json.Marshal(tabs)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(config.ReasonixHomeDir(), "desktop-tabs.json"), tabsBody, 0o600); err != nil {
		return err
	}
	digest := sha256.Sum256(registry)
	report := fixtureReport{Home: home, RegistryPath: registryPath, RegistrySHA256: hex.EncodeToString(digest[:]), TopicID: fixtureTopicID, VisibleText: fixtureText, ProjectRoot: projectRoot, LegacyPath: legacyPath}
	return writeJSON(reportPath, report)
}

func encodeLegacyHistory(messages ...legacyMessage) ([]byte, error) {
	var body strings.Builder
	encoder := json.NewEncoder(&body)
	for _, message := range messages {
		if err := encoder.Encode(message); err != nil {
			return nil, err
		}
	}
	return []byte(body.String()), nil
}

func createTopicDatabase(ctx context.Context, path, topicID, marker string) error {
	store, err := topicstate.Open(ctx, path)
	if err != nil {
		return err
	}
	if _, err := store.Update(ctx, topicID, func(record *topicstate.Record) {
		record.Title = fixtureTitle
		record.TitleSource = "manual"
	}); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	dsn, err := sqliteuri.Disk(path, url.Values{"_pragma": {"busy_timeout(5000)"}})
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, statement := range []string{
		`ALTER TABLE topics ADD COLUMN future_column TEXT NOT NULL DEFAULT ''`,
		`UPDATE topics SET future_column='future-` + marker + `' WHERE topic_id='` + topicID + `'`,
		`CREATE TABLE future_data(marker TEXT NOT NULL)`,
		`INSERT INTO future_data(marker) VALUES('` + marker + `')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func verifyFixture(ctx context.Context, reportPath, phase string) error {
	var report fixtureReport
	if err := readJSON(reportPath, &report); err != nil {
		return err
	}
	if err := verifyRegistryFields(report.RegistryPath); err != nil {
		return err
	}
	return verifyLegacySessionContinuity(ctx, report, reportPath, phase)
}

func verifyRegistryFields(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var registry struct {
		Version    int `json:"version"`
		FutureRoot struct {
			Preserve bool `json:"preserve"`
		} `json:"futureRoot"`
		Workspaces map[string]struct {
			FutureWorkspace struct {
				Preserve int `json:"preserve"`
			} `json:"futureWorkspace"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(body, &registry); err != nil {
		return err
	}
	if registry.Version != 3 || !registry.FutureRoot.Preserve || registry.Workspaces["global"].FutureWorkspace.Preserve != 42 {
		return errors.New("upgraded registry lost its version or unknown field values")
	}
	return nil
}

func verifyLegacySessionContinuity(ctx context.Context, report fixtureReport, reportPath, phase string) error {
	state, err := workspacestate.NewStore(report.RegistryPath).Load(ctx)
	if err != nil {
		return err
	}
	// Startup alone never imports the restored legacy tab; the restart launch
	// asks the banner to prepare it, which imports it into one session. The
	// JSONL bytes may be rewritten by shutdown, so authored content is checked.
	switch phase {
	case "first":
		if len(state.SourceMappings) != 0 || len(state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs) != 0 || len(state.PendingOperations) != 0 {
			return errors.New("startup imported or recreated the legacy session")
		}
		if err := verifyRestoredLegacyTab(report); err != nil {
			return err
		}
	case "restart":
		sessionID, err := verifyPreparedImport(&state, report)
		if err != nil {
			return err
		}
		if err := verifyPreparedTab(report, sessionID); err != nil {
			return err
		}
	default:
		return errors.New("--phase must be first or restart")
	}
	if err := verifyLegacyHistory(report.LegacyPath, fixtureQuestion, report.VisibleText); err != nil {
		return err
	}
	for path, marker := range map[string]string{config.DesktopTopicStatePath(""): "global", config.DesktopTopicStatePath(report.ProjectRoot): "project"} {
		if err := verifyTopicDatabase(ctx, path, marker); err != nil {
			return err
		}
	}
	if err := verifyBackups(ctx, report); err != nil {
		return err
	}
	resultPath := filepath.Join(filepath.Dir(reportPath), "verification-"+phase+".json")
	if phase == "restart" {
		var first struct {
			LegacyPath string `json:"legacyPath"`
		}
		if err := readJSON(filepath.Join(filepath.Dir(reportPath), "verification-first.json"), &first); err != nil {
			return err
		}
		if agent.CanonicalSessionPath(first.LegacyPath) != agent.CanonicalSessionPath(report.LegacyPath) {
			return errors.New("restart changed legacy session identity")
		}
	}
	return writeJSON(resultPath, map[string]any{"phase": phase, "version": state.Version, "legacyPath": report.LegacyPath, "history": report.VisibleText, "topicBackups": 2, "unknownData": "preserved", "verifiedAt": time.Now().UTC()})
}

// verifyPreparedImport returns the one global session the legacy source became.
func verifyPreparedImport(state *workspacestate.State, report fixtureReport) (string, error) {
	if len(state.SourceMappings) != 1 {
		return "", fmt.Errorf("source mappings=%d, want exactly the legacy source", len(state.SourceMappings))
	}
	var mapping workspacestate.SourceMapping
	for _, only := range state.SourceMappings {
		mapping = only
	}
	if mapping.Format != "legacy" || agent.CanonicalSessionPath(mapping.Path) != agent.CanonicalSessionPath(report.LegacyPath) {
		return "", fmt.Errorf("imported source %q (%s) is not the fixture legacy session", mapping.Path, mapping.Format)
	}
	sessions := state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs
	if mapping.SessionID == "" || mapping.WorkspaceID != workspacestate.GlobalWorkspaceID || len(sessions) != 1 || sessions[0] != mapping.SessionID {
		return "", fmt.Errorf("imported session %q in %q, global sessions=%v", mapping.SessionID, mapping.WorkspaceID, sessions)
	}
	for id, op := range state.PendingOperations {
		if op.Phase != "committed" {
			return "", fmt.Errorf("operation %s left open in phase %q", id, op.Phase)
		}
	}
	return mapping.SessionID, nil
}

func verifyPreparedTab(report fixtureReport, sessionID string) error {
	var saved struct {
		ActiveTab string `json:"activeTab"`
		Tabs      []struct {
			ID        string `json:"id"`
			TopicID   string `json:"topicId"`
			SessionID string `json:"sessionId"`
		} `json:"tabs"`
	}
	if err := readJSON(filepath.Join(config.ReasonixHomeDir(), "desktop-tabs.json"), &saved); err != nil {
		return err
	}
	for _, tab := range saved.Tabs {
		if tab.ID == saved.ActiveTab && tab.TopicID == report.TopicID && tab.SessionID == sessionID {
			return nil
		}
	}
	return errors.New("active tab does not carry the imported session and its topic")
}

func verifyLegacyHistory(path, question, answer string) error {
	messages, _, repairable, err := agent.LoadSessionDisplayMessages(path)
	if err != nil {
		return err
	}
	if !repairable {
		return errors.New("legacy history has a damaged authoritative tail")
	}
	authored := make([]provider.Message, 0, 2)
	for _, message := range messages {
		if message.Role == provider.RoleUser || message.Role == provider.RoleAssistant {
			authored = append(authored, message)
		}
	}
	if len(authored) != 2 || authored[0].Role != provider.RoleUser || authored[0].Content != question || authored[1].Role != provider.RoleAssistant || authored[1].Content != answer {
		return errors.New("legacy authored history changed")
	}
	return nil
}

func verifyRestoredLegacyTab(report fixtureReport) error {
	var saved struct {
		ActiveTab string `json:"activeTab"`
		Tabs      []struct {
			ID          string `json:"id"`
			TopicID     string `json:"topicId"`
			SessionPath string `json:"sessionPath"`
		} `json:"tabs"`
	}
	if err := readJSON(filepath.Join(config.ReasonixHomeDir(), "desktop-tabs.json"), &saved); err != nil {
		return err
	}
	for _, tab := range saved.Tabs {
		if tab.ID == saved.ActiveTab && tab.TopicID == report.TopicID && agent.CanonicalSessionPath(tab.SessionPath) == agent.CanonicalSessionPath(report.LegacyPath) {
			return nil
		}
	}
	return errors.New("restored tab lost its legacy source or topic")
}

func verifyBackups(ctx context.Context, report fixtureReport) error {
	backupRoot := filepath.Join(config.ReasonixHomeDir(), "desktop", "upgrade-backups")
	topicBackups, err := filepath.Glob(filepath.Join(backupRoot, "topics-*.sqlite"))
	if err != nil {
		return fmt.Errorf("list topic backups: %w", err)
	}
	if len(topicBackups) != 2 {
		return fmt.Errorf("topic backups=%d, want 2", len(topicBackups))
	}
	markers := map[string]bool{}
	for _, path := range topicBackups {
		marker, err := topicMarker(ctx, path)
		if err != nil {
			return err
		}
		markers[marker] = true
	}
	if !markers["global"] || !markers["project"] {
		return fmt.Errorf("topic backup markers=%v", markers)
	}
	// Startup may snapshot the newer registry too. Bind acceptance to the
	// immutable original instead of assuming no later snapshot can exist.
	backupPath := filepath.Join(backupRoot, "workspace-state-v1.json-"+report.RegistrySHA256+".bak")
	backupBody, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(backupBody)
	if hex.EncodeToString(digest[:]) != report.RegistrySHA256 {
		return errors.New("registry backup does not match the v1 source")
	}
	return nil
}

func verifyTopicDatabase(ctx context.Context, path, wantMarker string) error {
	marker, err := topicMarker(ctx, path)
	if err != nil {
		return err
	}
	if marker != wantMarker {
		return fmt.Errorf("topic database %s marker=%q, want %q", path, marker, wantMarker)
	}
	return nil
}

func topicMarker(ctx context.Context, path string) (string, error) {
	dsn, err := sqliteuri.Disk(path, url.Values{"mode": {"ro"}})
	if err != nil {
		return "", err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var marker, future string
	if err := db.QueryRowContext(ctx, `SELECT marker FROM future_data`).Scan(&marker); err != nil {
		return "", err
	}
	if err := db.QueryRowContext(ctx, `SELECT future_column FROM topics LIMIT 1`).Scan(&future); err != nil {
		return "", err
	}
	if future != "future-"+marker {
		return "", fmt.Errorf("future topic column=%q for marker %q", future, marker)
	}
	return marker, nil
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func readJSON(path string, value any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, value)
}
