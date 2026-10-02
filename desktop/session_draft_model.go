package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"reasonix/desktop/internal/draftstate"
	"reasonix/internal/config"
	"reasonix/internal/extension/providerext"
)

func draftSubmissionFingerprint(request SessionDraftSubmissionRequest) (string, string, error) {
	request.DraftID = strings.TrimSpace(request.DraftID)
	request.Display = strings.TrimSpace(request.Display)
	request.Input = strings.TrimSpace(request.Input)
	if request.Kind == "" {
		request.Kind = "turn"
	}
	data, err := json.Marshal(request)
	if err != nil {
		return "", "", err
	}
	// Request identity is transport metadata; equivalent concurrent requests
	// must still converge on the same operation.
	request.RequestID = ""
	request.SourceContentJSON = ""
	if request.Settings.ModelSource == draftModelSourceDefault {
		request.Settings.Model = ""
	}
	semantic, err := json.Marshal(request)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(semantic)
	return hex.EncodeToString(digest[:]), string(data), nil
}

const (
	draftModelSourceDefault  = "default"
	draftModelSourceExplicit = "explicit"
)

func (a *App) draftViewForOperation(record draftstate.Draft, op *draftstate.Operation) (SessionDraftView, error) {
	view, err := a.draftView(record)
	if err != nil || op == nil || op.Phase == "cancelled" || op.Phase == "terminal_failed" {
		return view, err
	}
	// A resumable submission owns a frozen model. The editor and picker must
	// show the same model that Continue will run, even after a default change.
	var request SessionDraftSubmissionRequest
	if json.Unmarshal([]byte(op.RequestJSON), &request) == nil && request.SnapshotVersion >= 3 &&
		request.SnapshotVersion <= draftstate.SnapshotVersion && request.Settings.Model != "" {
		view.Settings.Model = request.Settings.Model
	}
	return view, nil
}

func draftWorkspaceRoot(record draftstate.Draft) string {
	if record.Scope == "project" {
		return record.WorkspaceRoot
	}
	return globalWorkspaceRoot()
}

func (a *App) normalizeDraftSettingsForStorage(record draftstate.Draft, settings SessionDraftSettings) SessionDraftSettings {
	if settings.ModelSource == draftModelSourceDefault {
		// Keep a valid compatibility mirror for previous readers while the source
		// marker tells current readers to resolve the default live.
		settings.Model, _ = desktopNewSessionDefaults(record.Scope, draftWorkspaceRoot(record))
	}
	if settings.DisabledMCP == nil {
		settings.DisabledMCP = map[string]ServerView{}
	}
	if settings.MCPOrder == nil {
		settings.MCPOrder = []string{}
	}
	return settings
}

// migrateLegacyUntouchedDraftModel is the only safe legacy inference: revision
// one proves that no model pick or other edit was ever durably saved. Later
// legacy revisions keep their concrete model because its provenance is
// ambiguous and may represent an explicit user choice.
func (a *App) migrateLegacyUntouchedDraftModel(record draftstate.Draft) (draftstate.Draft, error) {
	if record.Revision != 1 || draftHasContent(record.ContentJSON) {
		return record, nil
	}
	var settings SessionDraftSettings
	if err := json.Unmarshal([]byte(record.SettingsJSON), &settings); err != nil {
		return record, err
	}
	if settings.ModelSource != "" || strings.TrimSpace(settings.Model) == "" {
		return record, nil
	}
	settings.ModelSource = draftModelSourceDefault
	settings = a.normalizeDraftSettingsForStorage(record, settings)
	payload, err := json.Marshal(settings)
	if err != nil {
		return record, err
	}
	upgraded, err := a.draftStore().Save(a.bootContext(), record.ID, record.Revision, record.ContentJSON, string(payload), false)
	if errors.Is(err, draftstate.ErrConflict) || errors.Is(err, draftstate.ErrOperationConflict) {
		// Submission owns the draft once an operation exists. Its request already
		// contains the frozen model, so migration must neither block recovery nor
		// revise the source snapshot under that operation.
		return a.draftStore().Get(a.bootContext(), record.ID)
	}
	return upgraded, err
}

func (a *App) freezeDraftSubmissionModel(record draftstate.Draft, view SessionDraftView, request SessionDraftSubmissionRequest) (SessionDraftSubmissionRequest, error) {
	if request.SnapshotVersion < 3 {
		request.Settings = view.Settings
		request.SnapshotVersion = 3
	}
	if request.Settings.ModelSource == draftModelSourceDefault {
		request.Settings.Model = view.Settings.Model
	}
	if providerext.PluginRefOwner(request.Settings.Model) != "" {
		return request, nil
	}
	cfg, err := config.LoadForRootReadOnly(draftWorkspaceRoot(record))
	if err != nil {
		return request, err
	}
	request.Settings.Model, err = resolveDraftCreateModelStrict(cfg, request.Settings.Model)
	return request, err
}
