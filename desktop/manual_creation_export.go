package main

import (
	"encoding/json"
	"time"
)

type manualCreationDiagnosticReport struct {
	Version    string                     `json:"version"`
	Commit     string                     `json:"commit"`
	ExportedAt string                     `json:"exportedAt"`
	Tasks      []manualCreationDiagnostic `json:"tasks"`
	Events     []manualCreationDiagnostic `json:"events"`
}

func (a *App) manualCreationDiagnosticReport() manualCreationDiagnosticReport {
	report := manualCreationDiagnosticReport{Version: version, Commit: buildCommit(), ExportedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Tasks: a.manualCreationDiagnostics(), Events: []manualCreationDiagnostic{}}
	a.manualCreationMu.Lock()
	m := a.manualCreations
	a.manualCreationMu.Unlock()
	if m != nil {
		m.mu.Lock()
		report.Events = append(report.Events, m.events...)
		m.mu.Unlock()
	}
	return report
}

// ExportManualCreationDiagnostics exports only allowlisted lifecycle metadata.
// It never reads chat content, configuration, attachments, or raw service logs.
func (a *App) ExportManualCreationDiagnostics() (string, error) {
	payload, err := json.MarshalIndent(a.manualCreationDiagnosticReport(), "", "  ")
	if err != nil {
		return "", err
	}
	path, err := a.PickExportFile("reasonix-creation-diagnostics-"+time.Now().Format("20060102-150405")+".json", "application/json")
	if err != nil || path == "" {
		return path, err
	}
	return path, a.SaveExportFile(path, string(payload), false)
}
