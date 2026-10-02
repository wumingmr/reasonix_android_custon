package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"reasonix/desktop/internal/hostrpc"
	"reasonix/internal/control"
	"reasonix/internal/secrets"
	"reasonix/internal/servecontract"
	"reasonix/internal/session"
)

func (a *App) writeSessionDiagnosticExport(job *sessionExportJob) error {
	browserDiagnostics := a.browserDiagnosticExport(job)
	extra := map[string]any{"sessionIdentity": map[string]any{"session": job.handle.Snapshot.Ref, "source": "local", "workspaceRoot": job.workspaceRoot, "storageGeneration": job.handle.Snapshot.StorageGeneration}, "exportSnapshot": job.handle.Snapshot, "frontendObservation": job.observation}
	var frontend map[string]json.RawMessage
	_ = json.Unmarshal(job.observation, &frontend)
	if value := frontend["readDiagnostics"]; len(value) > 0 {
		extra["readDiagnostics"] = value
	}
	if len(job.observation) == 0 {
		extra["frontendObservation"] = nil
	}
	return writeGoalDiagnosticsFile(job.path, func(dst io.Writer) error {
		if job.client != nil {
			extra["sessionIdentity"] = map[string]any{"session": job.handle.Snapshot.Ref, "source": "remote", "connectionHostId": job.sourceHostID, "workspaceRoot": job.workspaceRoot, "storageGeneration": job.handle.Snapshot.StorageGeneration}
			body, _ := json.Marshal(extra)
			resp, err := serveDoForSession(job.ctx, job.client, http.MethodPost, sessionExportURL(job.base, "/session-export/diagnostic", job.handle.Snapshot.Ref.SessionID, false), body, job.route)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return errors.New("remote session diagnostics are unavailable; upgrade the remote service if the session was switched or taken over")
			}
			if _, err = copyExportContext(job.ctx, dst, resp.Body); err != nil {
				return err
			}
			encoded, err := json.Marshal(browserDiagnostics)
			if err != nil {
				return err
			}
			encoded = []byte(secrets.Redact(string(encoded)))
			if !json.Valid(encoded) {
				return errors.New("redacted browser diagnostics are invalid")
			}
			return appendBrowserDiagnosticSection(dst, encoded)
		}
		extra["browserDiagnostics"] = browserDiagnostics
		metadata := control.GoalDiagnosticMetadata{ApplicationVersion: version, BuildCommit: buildCommit(), ProtocolVersion: hostrpc.ProtocolVersion, Capabilities: []string{servecontract.SessionExportV1, servecontract.GoalLifecycleV2}}
		if a.sessionDiagnosticControllerCurrent(job.controller, job.handle.Snapshot.Ref) {
			runtimeErr := job.controller.WriteSessionDiagnostics(job.ctx, dst, metadata, extra)
			if a.sessionDiagnosticControllerCurrent(job.controller, job.handle.Snapshot.Ref) {
				return runtimeErr
			}
			file, ok := dst.(*os.File)
			if !ok {
				return errors.Join(runtimeErr, errors.New("session controller changed during diagnostic capture"))
			}
			if resetErr := file.Truncate(0); resetErr != nil {
				return errors.Join(runtimeErr, resetErr)
			}
			if _, resetErr := file.Seek(0, io.SeekStart); resetErr != nil {
				return errors.Join(runtimeErr, resetErr)
			}
		}
		return control.WriteColdSessionDiagnostics(job.ctx, dst, job.query, job.handle.Snapshot, metadata, extra)
	})
}

func (a *App) sessionDiagnosticControllerCurrent(controller *control.Controller, ref session.SessionRef) bool {
	if controller == nil {
		return false
	}
	boundRef, bound := controller.SessionRef()
	if !bound || boundRef != ref {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.tabs {
		if !tab.removed && tab.Ctrl == controller && tab.SessionID == ref.SessionID {
			return true
		}
	}
	return false
}
