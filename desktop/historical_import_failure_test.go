package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestHistoricalPreparationFailureCarriesItsCause(t *testing.T) {
	isolateDesktopUserDirs(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	dir := desktopSessionDir(root)
	path := filepath.Join(dir, "20260801-101010-deepseek.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "q", Role: provider.RoleUser, Content: "question"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(base, "elsewhere")
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{Scope: "project", WorkspaceRoot: elsewhere}); err != nil {
		t.Fatal(err)
	}
	app := newHistoricalLifecycleApp(t)
	installSessionCatalogForTest(t, app, dir, "project", root)
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].Source == nil {
		t.Fatalf("historical source missing: %+v %v", page, err)
	}
	view, err := app.PrepareSession(SessionSelector{Source: page.Items[0].Source})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !terminalPreparationStatus(view.Status) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if view, err = app.GetSessionPreparation(view.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	if view.Status != "failed" || view.ErrorCode != "workspace_conflict" {
		t.Fatalf("preparation must name the workspace conflict, got %+v", view)
	}
	if !strings.Contains(view.ErrorDetail, errSessionWorkspaceConflict.Error()) {
		t.Fatalf("preparation must carry the failure detail, got %q", view.ErrorDetail)
	}
	status := app.GetHistoricalImportStatus()
	var listed *HistoricalSessionView
	for i := range status.Items {
		if status.Items[i].ID == view.SourceKey {
			listed = &status.Items[i]
		}
	}
	if listed == nil || listed.ErrorCode != "workspace_conflict" || listed.ErrorDetail != view.ErrorDetail {
		t.Fatalf("recovery list must show the same cause, got %+v", listed)
	}
}

func terminalPreparationStatus(status string) bool {
	return status == "ready" || status == "failed" || status == "blocked" || status == "cancelled"
}

func TestHistoricalImportFailureCodeReadsIdentity(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("wrap: %w", errSessionWorkspaceConflict), "workspace_conflict"},
		{fmt.Errorf("replay: %w", agent.ErrSessionReplayLimitExceeded), "history_too_large"},
		{errors.Join(errors.New("ledger"), agent.ErrSessionHistoryDamaged), "history_damaged"},
		{fmt.Errorf("open: %w", session.ErrDamagedStore), "history_damaged"},
		{fmt.Errorf("open: %w", session.ErrUnsupportedVersion), "unsupported_version"},
		{fmt.Errorf("resolve: %w", workspacestate.ErrMutationConflict), "state_conflict"},
		{newSessionOperationError("target_changed", "changed"), "target_changed"},
		{fmt.Errorf("lease: %w", session.ErrWriterOwned), "source_busy"},
		{errors.New("disk full"), "import_failed"},
	} {
		if got := historicalImportFailureCode(tc.err); got != tc.want {
			t.Errorf("%v: code %q, want %q", tc.err, got, tc.want)
		}
	}
	long := strings.Repeat("历", historicalFailureDetailLimit)
	detail := historicalImportFailureDetail(errors.New(long))
	if len(detail) > historicalFailureDetailLimit+len("…") || !strings.HasSuffix(detail, "…") || !strings.HasPrefix(long, strings.TrimSuffix(detail, "…")) {
		t.Fatalf("detail not bounded on a rune boundary: %d bytes", len(detail))
	}
}
