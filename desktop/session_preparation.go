package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

// SessionPreparationView is the revisioned snapshot shared by navigation and
// storage management. Scheduling state intentionally stays outside the durable
// import lifecycle ledger.
type SessionPreparationView struct {
	OperationID string              `json:"operationId"`
	SourceKey   string              `json:"sourceKey"`
	Status      string              `json:"status"`
	Revision    uint64              `json:"revision"`
	Target      *session.SessionRef `json:"target,omitempty"`
	ErrorCode   string              `json:"errorCode,omitempty"`
	ErrorDetail string              `json:"errorDetail,omitempty"`
	Retryable   bool                `json:"retryable"`
}

type HistoricalSourceUpdateView struct {
	SourceKey string              `json:"sourceKey"`
	Status    string              `json:"status"`
	Version   string              `json:"version,omitempty"`
	Target    *session.SessionRef `json:"target,omitempty"`
	Source    *SessionSourceRef   `json:"source,omitempty"`
	ErrorCode string              `json:"errorCode,omitempty"`
	Retryable bool                `json:"retryable"`
}

type historicalSourceUpdateCall struct {
	view      HistoricalSourceUpdateView
	done      chan struct{}
	delivered bool
}

func preparationSnapshot(call *historicalImportCall) SessionPreparationView {
	view := SessionPreparationView{OperationID: call.operationID, SourceKey: call.sourceKey, Status: call.status,
		Revision: call.revision, ErrorCode: call.errorCode, ErrorDetail: call.errorDetail,
		Retryable: call.status == "blocked" || call.status == "failed" || call.status == "cancelled"}
	if call.status == "ready" {
		ref := call.result.Session
		view.Target = &ref
	}
	return view
}

func (a *App) historicalSourceForSelector(selector SessionSelector) (string, historicalSource, error) {
	if selector.Source != nil {
		ref := selector.Source
		if strings.TrimSpace(ref.Path) == "" {
			return "", historicalSource{}, newSessionOperationError(sessionOperationTargetNotFound, "The source no longer exists.")
		}
		id := desktopSourceKey(ref.Path, ref.HeadID)
		state, loadErr := a.workspaceRegistry().Load(a.bootContext())
		if loadErr != nil {
			return "", historicalSource{}, loadErr
		}
		if ref.SourceKey != "" && !slices.Contains(state.SourceKeys(ref.SourceKey), id) {
			return "", historicalSource{}, newSessionOperationError("target_changed", "The source identity changed.")
		}
		mapping, adopted, resolveErr := state.ResolveSource(id)
		if resolveErr != nil {
			return "", historicalSource{}, resolveErr
		}
		format, scope, root := "legacy", "global", ""
		if info, statErr := os.Stat(ref.Path); statErr == nil && info.IsDir() {
			format = "canonical"
		}
		if adopted {
			id = mapping.SourceKey
			workspace := state.Workspaces[mapping.WorkspaceID]
			root = workspace.Root
			if mapping.WorkspaceID != "global" {
				scope = "project"
			}
		}
		if root == "" {
			for _, project := range loadProjectsFile().Projects {
				if strings.HasPrefix(cleanDesktopPath(ref.Path), cleanDesktopPath(desktopSessionDir(project.Root))) {
					scope, root = "project", project.Root
					break
				}
			}
		}
		return id, historicalSource{path: ref.Path, head: ref.HeadID, format: format, scope: scope, root: root}, nil
	}
	target, err := a.resolveSessionTarget(selector)
	if err != nil {
		return "", historicalSource{}, err
	}
	if target.Source == nil {
		state, loadErr := a.workspaceRegistry().Load(a.bootContext())
		if loadErr != nil {
			return "", historicalSource{}, loadErr
		}
		keys := make([]string, 0)
		for key, mapping := range state.SourceMappings {
			if mapping.SessionID == target.SessionRef.SessionID && !strings.Contains(key, ":review:") {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return "", historicalSource{}, newSessionOperationError("unsupported", "This session has no historical source.")
		}
		mapping := state.SourceMappings[keys[0]]
		workspace := state.Workspaces[mapping.WorkspaceID]
		scope := "project"
		if mapping.WorkspaceID == "global" {
			scope = "global"
		}
		return keys[0], historicalSource{path: mapping.Path, head: mapping.HeadID, format: mapping.Format,
			scope: scope, root: workspace.Root}, nil
	}
	id := strings.TrimSpace(target.Source.SourceKey)
	if id == "" {
		id = desktopSourceKey(target.Source.Path, target.Source.HeadID)
	}
	format := "legacy"
	if info, statErr := os.Stat(target.Source.Path); statErr == nil && info.IsDir() {
		format = "canonical"
	}
	return id, historicalSource{path: target.Source.Path, head: target.Source.HeadID, format: format,
		scope: target.Scope, root: target.WorkspaceRoot}, nil
}

// PrepareSession starts or joins preparation and returns immediately. Canonical
// sessions are already ready and never enter the historical queue.
func (a *App) PrepareSession(selector SessionSelector) (SessionPreparationView, error) {
	if selector.Ref != nil {
		target, err := a.resolveSessionTarget(selector)
		if err != nil {
			return SessionPreparationView{}, err
		}
		ref := target.SessionRef
		return SessionPreparationView{OperationID: "ready-" + ref.SessionID, Status: "ready", Revision: target.LifecycleGeneration, Target: &ref}, nil
	}
	id, source, err := a.historicalSourceForSelector(selector)
	if err != nil {
		return SessionPreparationView{}, err
	}
	_, listErr := a.ListHistoricalSessions()
	c := &a.historicalImports
	c.mu.Lock()
	c.initialize(a.bootContext())
	if _, exists := c.sources[id]; !exists {
		c.sources[id] = source
		c.views[id] = historicalImportViewFromSource(id, source)
	}
	c.mu.Unlock()
	if listErr != nil && source.path == "" {
		return SessionPreparationView{}, listErr
	}
	call, err := a.prepareHistoricalSession(id, true, false)
	if err != nil {
		return SessionPreparationView{}, err
	}
	c.mu.Lock()
	view := preparationSnapshot(call)
	c.mu.Unlock()
	return view, nil
}

func historicalImportViewFromSource(id string, source historicalSource) HistoricalSessionView {
	return HistoricalSessionView{ID: id, Title: filepathBaseOrFallback(source.path), Format: source.format, Status: "available",
		Source: &SessionSourceRef{HostID: localDesktopHostID, SourceKey: desktopSourceKey(source.path, source.head), Path: source.path, HeadID: source.head}}
}

func filepathBaseOrFallback(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "Historical session"
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	return parts[len(parts)-1]
}

func (a *App) GetSessionPreparation(operationID string) (SessionPreparationView, error) {
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initialize(a.bootContext())
	call := c.operations[strings.TrimSpace(operationID)]
	if call == nil {
		return SessionPreparationView{}, newSessionOperationError(sessionOperationTargetNotFound, "The preparation task no longer exists.")
	}
	return preparationSnapshot(call), nil
}

func (a *App) CancelSessionPreparation(operationID string) (SessionPreparationView, error) {
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	call := c.operations[strings.TrimSpace(operationID)]
	if call == nil {
		return SessionPreparationView{}, newSessionOperationError(sessionOperationTargetNotFound, "The preparation task no longer exists.")
	}
	if call.status == "ready" {
		return preparationSnapshot(call), nil
	}
	call.interactive = false
	if !call.batch && (call.status == "queued" || call.status == "preparing") {
		call.cancel()
	}
	return preparationSnapshot(call), nil
}

// CheckHistoricalSourceUpdate obtains the same non-blocking ownership used by
// conversion. Metadata changes alone are ignored because the durable-content
// fingerprint excludes branch presentation sidecars.
func (a *App) CheckHistoricalSourceUpdate(selector SessionSelector) (HistoricalSourceUpdateView, error) {
	id, source, err := a.historicalSourceForSelector(selector)
	if err != nil {
		return HistoricalSourceUpdateView{}, err
	}
	c := &a.historicalImports
	c.mu.Lock()
	c.initialize(a.bootContext())
	if call := c.updates[id]; call != nil {
		if call.view.Status != "checking" && call.delivered {
			delete(c.updates, id)
		} else {
			if call.view.Status != "checking" {
				call.delivered = true
			}
			view := call.view
			c.mu.Unlock()
			return view, nil
		}
	}
	call := &historicalSourceUpdateCall{view: HistoricalSourceUpdateView{SourceKey: id, Status: "checking"}, done: make(chan struct{})}
	c.updates[id] = call
	c.workers.Add(1)
	ctx := c.ctx
	initial := call.view
	c.mu.Unlock()
	go func() {
		defer c.workers.Done()
		var view HistoricalSourceUpdateView
		select {
		case c.updateWorker <- struct{}{}:
			defer func() { <-c.updateWorker }()
			view = a.checkHistoricalSourceUpdate(ctx, id, source)
		case <-ctx.Done():
			view = HistoricalSourceUpdateView{SourceKey: id, Status: "cancelled", Retryable: true}
		}
		c.mu.Lock()
		call.view = view
		close(call.done)
		c.mu.Unlock()
	}()
	return initial, nil
}

func (a *App) checkHistoricalSourceUpdate(ctx context.Context, id string, source historicalSource) HistoricalSourceUpdateView {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return HistoricalSourceUpdateView{SourceKey: id, Status: "failed", ErrorCode: "registry_unavailable", Retryable: true}
	}
	mapping, ok, resolveErr := state.ResolveSource(id)
	if resolveErr != nil {
		return HistoricalSourceUpdateView{SourceKey: id, Status: "failed", ErrorCode: "target_changed"}
	}
	if !ok {
		return HistoricalSourceUpdateView{SourceKey: id, Status: "not_prepared", Retryable: true}
	}
	release, err := acquireHistoricalSource(ctx, id, source)
	if err != nil {
		if historicalSourceBusyError(err) {
			return HistoricalSourceUpdateView{SourceKey: id, Status: "blocked", ErrorCode: "source_busy", Retryable: true}
		}
		return HistoricalSourceUpdateView{SourceKey: id, Status: "failed", ErrorCode: "check_failed", Retryable: true}
	}
	defer release()
	fingerprint, err := desktopSourceFingerprint(source.path)
	if err != nil {
		return HistoricalSourceUpdateView{SourceKey: id, Status: "failed", ErrorCode: "source_unavailable", Retryable: true}
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	status := "unchanged"
	if state.SessionStates[mapping.SessionID].Lifecycle != workspacestate.Active {
		status = "retired"
	}
	if fingerprint != mapping.Fingerprint {
		status = "available"
	}
	sourceRef := SessionSourceRef{HostID: localDesktopHostID, SourceKey: desktopSourceKey(source.path, source.head), Path: source.path, HeadID: source.head}
	return HistoricalSourceUpdateView{SourceKey: id, Status: status, Version: fingerprint, Target: &ref, Source: &sourceRef}
}

func (a *App) PrepareHistoricalSourceVersion(sourceRef SessionSourceRef, version string) (SessionPreparationView, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return SessionPreparationView{}, errors.New("historical source version is required")
	}
	id, source, err := a.historicalSourceForSelector(SessionSelector{Source: &sourceRef})
	if err != nil {
		return SessionPreparationView{}, err
	}
	release, err := acquireHistoricalSource(a.bootContext(), id, source)
	if err != nil {
		return SessionPreparationView{}, err
	}
	current, fingerprintErr := desktopSourceFingerprint(source.path)
	release()
	if fingerprintErr != nil {
		return SessionPreparationView{}, fingerprintErr
	}
	if current != version {
		return SessionPreparationView{}, newSessionOperationError("target_changed", "The historical source changed. Check for updates again.")
	}
	versionID := id + ":review:" + version
	source.version = version
	c := &a.historicalImports
	c.mu.Lock()
	c.initialize(a.bootContext())
	c.sources[versionID] = source
	c.views[versionID] = historicalImportViewFromSource(versionID, source)
	c.mu.Unlock()
	call, err := a.prepareHistoricalSession(versionID, true, false)
	if err != nil {
		return SessionPreparationView{}, err
	}
	c.mu.Lock()
	view := preparationSnapshot(call)
	c.mu.Unlock()
	return view, nil
}
