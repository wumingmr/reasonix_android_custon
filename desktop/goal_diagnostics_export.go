package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"reasonix/desktop/internal/hostrpc"
	"reasonix/internal/control"
	"reasonix/internal/fileutil"
	"reasonix/internal/servecontract"
)

// ExportGoalDiagnostics lets the user save the authoritative event stream,
// including tool calls/results and the exact goal/runtime identity, without
// relying on the frontend's paginated transcript.
func (a *App) ExportGoalDiagnostics() (string, error) {
	tab, api := a.activeTabAndCtrl()
	if tab == nil {
		return "", errors.New("goal diagnostics are unavailable for this session")
	}
	// New peers and local canonical sessions share the explicit export owner.
	if !a.isRemoteTab(tab.ID) || a.remoteSessionExportSupported(tab.ID) {
		handle, err := a.BeginSessionExportForTarget(SessionSelector{}, tab.ID, "diagnostic", tab.TopicTitle, "")
		if err != nil || handle.ExportID == "" {
			return "", err
		}
		defer func() { _ = a.CancelSessionExport(handle.ExportID) }()
		result, err := a.FinishSessionExport(handle.ExportID)
		if err != nil {
			return "", err
		}
		if len(result.Paths) == 0 {
			return "", nil
		}
		return result.Paths[0], nil
	}
	path, err := a.nativeHost().SaveFileDialog(a.ctx, nativeDialogOptions{
		Title:                "Export goal diagnostics",
		DefaultDirectory:     dialogDefaultDirectory(tab.WorkspaceRoot),
		DefaultFilename:      safeExportFilename(tab.TopicTitle + "-goal-diagnostics.json"),
		CanCreateDirectories: true,
		Filters:              exportFileFilters("application/json", ".json"),
	})
	if err != nil || path == "" {
		return "", err
	}
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	ctrl, local := api.(*control.Controller)
	if local && ctrl != nil {
		err = writeGoalDiagnosticsFile(path, func(dst io.Writer) error {
			return ctrl.WriteGoalDiagnostics(a.ctx, dst, control.GoalDiagnosticMetadata{
				ApplicationVersion: version,
				BuildCommit:        buildCommit(),
				ProtocolVersion:    hostrpc.ProtocolVersion,
				Capabilities:       []string{"session-history-v1", "session-identity-v1", servecontract.GoalLifecycleV2},
			})
		})
	} else if a.isRemoteTab(tab.ID) {
		err = writeGoalDiagnosticsFile(path, func(dst io.Writer) error {
			return a.writeRemoteGoalDiagnostics(tab.ID, dst)
		})
	} else {
		err = errors.New("goal diagnostics are unavailable for this session")
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) exportRemoteGoalDiagnostics(tabID string) ([]byte, error) {
	var output bytes.Buffer
	if err := a.writeRemoteGoalDiagnostics(tabID, &output); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (a *App) writeRemoteGoalDiagnostics(tabID string, dst io.Writer) error {
	if err := a.requireRemoteGoalLifecycle(tabID); err != nil {
		return err
	}
	client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
	if err != nil {
		return err
	}
	resp, err := serveDoForSession(a.ctx, client, http.MethodGet, serveURL(base, "/goal-diagnostics"), nil, expectedPath)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return fmt.Errorf("export remote goal diagnostics: status %d: %s", resp.StatusCode, string(data))
	}
	_, err = io.Copy(dst, resp.Body)
	if err != nil {
		return fmt.Errorf("read remote goal diagnostics: %w", err)
	}
	return nil
}

func writeGoalDiagnosticsFile(path string, write func(io.Writer) error) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".reasonix-goal-diagnostics-*")
	if err != nil {
		return exportOperationError("save goal diagnostics", path, err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return exportOperationError("save goal diagnostics", path, err)
	}
	if err := write(tmp); err != nil {
		return exportOperationError("save goal diagnostics", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return exportOperationError("save goal diagnostics", path, err)
	}
	if err := tmp.Close(); err != nil {
		return exportOperationError("save goal diagnostics", path, err)
	}
	if err := fileutil.ReplaceFile(tmpPath, path); err != nil {
		return exportOperationError("save goal diagnostics", path, err)
	}
	keep = true
	return nil
}

func (a *App) remoteSessionExportSupported(tabID string) bool {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	tab := a.remoteTabs[tabID]
	return tab != nil && tab.capabilities[servecontract.SessionExportV1]
}
