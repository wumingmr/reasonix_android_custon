package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/historywork"
	"reasonix/internal/identitylock"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

var errHistoricalSourceBusy = errors.New("historical session is in use; close the other instance and retry")

type HistoricalSessionView struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Format      string              `json:"format"`
	Status      string              `json:"status"`
	ErrorCode   string              `json:"errorCode,omitempty"`
	ErrorDetail string              `json:"errorDetail,omitempty"`
	Session     *session.SessionRef `json:"session,omitempty"`
	Source      *SessionSourceRef   `json:"source,omitempty"`
}

type HistoricalImportStatus struct {
	Items     []HistoricalSessionView `json:"items"`
	Running   bool                    `json:"running"`
	Paused    bool                    `json:"paused"`
	Remaining int                     `json:"remaining"`
	Completed int                     `json:"completed"`
	Blocked   int                     `json:"blocked"`
	Failed    int                     `json:"failed"`
}

type historicalSource struct {
	path, format, scope, root, head, version string
}
type historicalImportCall struct {
	operationID        string
	sourceKey          string
	ctx                context.Context
	cancel             context.CancelFunc
	done               chan struct{}
	result             SessionRestoreResult
	err                error
	status             string
	errorCode          string
	errorDetail        string
	revision           uint64
	interactive, batch bool
}
type historicalImportCoordinator struct {
	mu                       sync.Mutex
	discoveryMu              sync.Mutex
	discoveryPending         bool
	legacyReconcile          historicalLegacyReconcileState
	catalogEnabled           bool
	catalogAt                time.Time
	catalogRevision          uint64
	catalog                  []historicalCatalogEntry
	sources                  map[string]historicalSource
	views                    map[string]HistoricalSessionView
	calls                    map[string]*historicalImportCall
	operations               map[string]*historicalImportCall
	updates                  map[string]*historicalSourceUpdateCall
	updateWorker             chan struct{}
	revision                 uint64
	ctx                      context.Context
	cancel                   context.CancelFunc
	queue                    []string
	current                  string
	queueLoaded              bool
	queueRevision            uint64
	queueRelease             func()
	presentations            map[string]historicalSourcePresentation
	unavailableSources       map[string]bool
	running, paused, stopped bool
	wake                     chan struct{}
	workers                  sync.WaitGroup
}

func (a *App) GetHistoricalImportStatus() HistoricalImportStatus {
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status()
}

func (a *App) historicalPreparationStatus(sourceKey string) string {
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	if view, ok := c.views[sourceKey]; ok {
		return view.Status
	}
	return "available"
}

// Listing reads directory entries and registry metadata only.
func (a *App) ListHistoricalSessions() (HistoricalImportStatus, error) {
	return a.listHistoricalSessions(a.bootContext())
}

func scanHistoricalRoot(ctx context.Context, source desktopMigrationSource, format string, add func(string, string, string, string, string), coordinators ...*historywork.Coordinator) error {
	f, err := os.Open(source.root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		release := func(int64) {}
		if len(coordinators) > 0 && coordinators[0] != nil {
			release, err = coordinators[0].BackgroundSlice(ctx, false)
			if err != nil {
				return err
			}
		}
		bytes, count := int64(0), 0
		started := time.Now()
		var readErr error
		for count < historywork.BatchEntries && bytes+historywork.ReadChunk <= historywork.BatchBytes && time.Since(started) < historywork.SliceDuration {
			if readErr = ctx.Err(); readErr != nil {
				break
			}
			var entries []os.DirEntry
			entries, readErr = f.ReadDir(1)
			if readErr != nil {
				break
			}
			count++
			entry := entries[0]
			if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if format == "canonical" && !entry.IsDir() {
				continue
			}
			if format == "legacy" && (entry.IsDir() || !store.IsSessionTranscriptName(entry.Name())) {
				continue
			}
			path := filepath.Join(source.root, entry.Name())
			if format == "canonical" && !hasHistoricalSessionArtifacts(path) {
				continue
			}
			if format == "legacy" {
				// The bounded head sidecar is the only payload this discovery reads.
				// Charge its maximum size so errors and concurrent changes cannot
				// exceed the shared rate allowance.
				bytes += historywork.ReadChunk
				if addIndexedHistoricalHeads(path, source, add) {
					continue
				}
			}
			add(path, format, source.scope, source.workspaceRoot, "")
		}
		release(bytes)
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if len(coordinators) == 0 || coordinators[0] == nil {
			if err := historywork.Pause(ctx, bytes); err != nil {
				return err
			}
		}
	}
}

func addIndexedHistoricalHeads(path string, source desktopMigrationSource, add func(string, string, string, string, string)) bool {
	if info, err := os.Stat(store.SessionEventIndex(path)); err != nil || info.Size() > historywork.ReadChunk {
		return false
	}
	index, err := agent.ReadSessionHeadIndex(path)
	if err != nil || index == nil || !index.Current(path) {
		return false
	}
	selected := ""
	for _, head := range index.Heads {
		if !head.Retired && head.Selected {
			selected = head.ID
		}
	}
	add(path, "legacy", source.scope, source.workspaceRoot, "")
	for _, head := range index.Heads {
		if !head.Retired && head.ID != "" && head.ID != selected {
			add(path, "legacy", source.scope, source.workspaceRoot, head.ID)
		}
	}
	return true
}

func addHistoricalRegistrySources(state workspacestate.State, add func(string, string, string, string, string)) {
	workspaceSource := func(path, format, workspaceID, head string) {
		w := state.Workspaces[workspaceID]
		scope := "project"
		if workspaceID == "global" {
			scope = "global"
		}
		add(path, format, scope, w.Root, head)
	}
	for _, mapping := range state.SourceMappings {
		workspaceSource(mapping.Path, mapping.Format, mapping.WorkspaceID, mapping.HeadID)
	}
	for _, op := range state.PendingOperations {
		if op.Mapping != nil && (op.Kind == "import" || op.Kind == "restore") {
			workspaceSource(op.Mapping.Path, op.Mapping.Format, op.WorkspaceID, op.Mapping.HeadID)
		}
	}
}

func historicalImportView(state workspacestate.State, id string, source historicalSource, view HistoricalSessionView) HistoricalSessionView {
	if view.ID == "" {
		view = HistoricalSessionView{ID: id, Title: filepath.Base(source.path), Format: source.format, Status: "available"}
	}
	view.Source = &SessionSourceRef{HostID: localDesktopHostID, SourceKey: desktopSourceKey(source.path, source.head), Path: source.path, HeadID: source.head}
	mapping, ok, err := historicalMappingForSource(state, id)
	if err != nil {
		view.Status, view.ErrorCode, view.ErrorDetail, view.Session = "failed", "target_changed", "", nil
		return view
	}
	if !ok {
		return view
	}
	view.Status = "imported"
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	view.Session = &ref
	if lifecycle := state.SessionStates[mapping.SessionID].Lifecycle; lifecycle == workspacestate.Deleted || lifecycle == workspacestate.Archived {
		view.Status = strings.ToLower(lifecycle)
		view.Session = nil
	}
	return view
}

func (c *historicalImportCoordinator) initialize(ctx context.Context) {
	if c.sources != nil {
		if !c.stopped && !c.running && len(c.calls) == 0 && c.ctx.Err() != nil {
			c.ctx, c.cancel = context.WithCancel(ctx)
		}
		return
	}
	c.sources = map[string]historicalSource{}
	c.views = map[string]HistoricalSessionView{}
	c.calls = map[string]*historicalImportCall{}
	c.operations = map[string]*historicalImportCall{}
	c.updates = map[string]*historicalSourceUpdateCall{}
	c.updateWorker = make(chan struct{}, 1)
	c.presentations = map[string]historicalSourcePresentation{}
	c.unavailableSources = map[string]bool{}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.wake = make(chan struct{}, 1)
}
func (c *historicalImportCoordinator) status() HistoricalImportStatus {
	out := HistoricalImportStatus{Items: []HistoricalSessionView{}, Running: c.running, Paused: c.paused, Remaining: len(c.queue)}
	if c.current != "" {
		out.Remaining++
	}
	for _, view := range c.views {
		out.Items = append(out.Items, view)
		switch view.Status {
		case "imported":
			out.Completed++
		case "blocked":
			out.Blocked++
		case "failed":
			out.Failed++
		}
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	return out
}

func (a *App) ImportHistoricalSession(id string) (SessionRestoreResult, error) {
	_, listErr := a.ListHistoricalSessions()
	if listErr != nil {
		// A damaged or inaccessible historical root must not make healthy
		// sources unusable. The requested source is checked below; callers can
		// still inspect the list's per-source status for the affected root.
		c := &a.historicalImports
		c.mu.Lock()
		_, known := c.sources[id]
		c.mu.Unlock()
		if !known {
			return SessionRestoreResult{}, listErr
		}
	}
	call, err := a.prepareHistoricalSession(id, true, false)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	return waitHistoricalImport(call)
}

// Duplicate requests join one import. Interactive navigation and the bulk queue
// hold independent demands so cancelling one cannot abort the other.
func (a *App) prepareHistoricalSession(id string, interactive, batch bool) (*historicalImportCall, error) {
	c := &a.historicalImports
	c.mu.Lock()
	if c.stopped || a.shuttingDown.Load() {
		c.mu.Unlock()
		return nil, context.Canceled
	}
	source, ok := c.sources[id]
	if !ok {
		c.mu.Unlock()
		return nil, errors.New("historical session is unavailable; refresh the list")
	}
	if call := c.calls[id]; call != nil {
		call.interactive = call.interactive || interactive
		call.batch = call.batch || batch
		c.mu.Unlock()
		return call, nil
	}
	for operationID, previous := range c.operations {
		if previous.sourceKey == id {
			if previous.status == "ready" {
				c.mu.Unlock()
				return previous, nil
			}
			delete(c.operations, operationID)
		}
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.revision++
	operationID := "prepare-" + strings.TrimPrefix(newTabID(), "tab_")
	call := &historicalImportCall{operationID: operationID, sourceKey: id, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), status: "queued", revision: c.revision, interactive: interactive, batch: batch}
	c.calls[id] = call
	c.operations[call.operationID] = call
	view := c.views[id]
	view.Status, view.ErrorCode, view.ErrorDetail = "queued", "", ""
	c.views[id] = view
	c.workers.Add(1)
	c.mu.Unlock()
	go a.runHistoricalPreparation(call, id, source)
	return call, nil
}

func waitHistoricalImport(call *historicalImportCall) (SessionRestoreResult, error) {
	select {
	case <-call.done:
		return call.result, call.err
	case <-call.ctx.Done():
		<-call.done
		return call.result, call.err
	}
}

func (a *App) runHistoricalPreparation(call *historicalImportCall, id string, source historicalSource) {
	c := &a.historicalImports
	defer c.workers.Done()
	c.mu.Lock()
	c.revision++
	call.status, call.revision = "preparing", c.revision
	view := c.views[id]
	view.Status, view.ErrorCode, view.ErrorDetail = "importing", "", ""
	c.views[id] = view
	c.mu.Unlock()
	result, err := a.importHistoricalSource(call.ctx, id, source)
	if err != nil && !errors.Is(err, context.Canceled) && !historicalSourceBusyError(err) {
		// Preparation can fail before the archive/open mutation is reached.
		// Keep its cause in the local host log, correlated by opaque source ID.
		slog.Warn("desktop: historical session preparation failed", "source_key", id,
			"operation", call.operationID, "format", source.format, "err", err)
	}
	var presentationErr error
	if err == nil {
		presentationErr = a.applyHistoricalSourcePresentation(desktopSourceKey(source.path, source.head), result.Session)
	}
	c.mu.Lock()
	if c.calls[id] != call {
		call.result, call.err = result, err
		close(call.done)
		c.mu.Unlock()
		return
	}
	call.result, call.err = result, err
	view = c.views[id]
	if err == nil {
		view.Status, call.status = "imported", "ready"
		ref := result.Session
		view.Session = &ref
		if presentationErr != nil {
			view.ErrorCode = "presentation_pending"
		}
	} else {
		code := historicalImportFailureCode(err)
		view.Status, call.status = "failed", "failed"
		switch code {
		case "source_busy":
			view.Status, call.status = "blocked", "blocked"
			err = errHistoricalSourceBusy
		case "cancelled":
			view.Status, call.status = "available", "cancelled"
		}
		view.ErrorCode, call.errorCode = code, code
		view.ErrorDetail, call.errorDetail = historicalImportFailureDetail(err), historicalImportFailureDetail(err)
		call.err = err
	}
	c.revision++
	call.revision = c.revision
	c.views[id] = view
	delete(c.calls, id)
	close(call.done)
	c.mu.Unlock()
	a.emitProjectTreeChanged()
}

func (a *App) saveHistoricalSourcePresentation(sourceKey string, update func(*historicalSourcePresentation)) error {
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initialize(a.bootContext())
	if !c.queueLoaded {
		c.loadQueueLocked()
	}
	return updateHistoricalSidecar(func(saved *historicalImportQueueSidecar) error {
		presentation := saved.Presentations[sourceKey]
		update(&presentation)
		saved.Presentations[sourceKey] = presentation
		c.presentations = saved.Presentations
		return nil
	})
}

func (a *App) applyHistoricalSourcePresentation(sourceKey string, ref session.SessionRef) error {
	saved, err := readHistoricalSidecar()
	if err != nil {
		return err
	}
	presentation, ok := saved.Presentations[sourceKey]
	if !ok {
		return nil
	}
	var joined error
	if presentation.Title != "" {
		joined = errors.Join(joined, a.desktopSessionService("").SetTitle(a.bootContext(), ref, presentation.Title))
	}
	if presentation.Pinned != nil {
		joined = errors.Join(joined, a.workspaceRegistry().UpdatePresentation(a.bootContext(), []string{ref.SessionID}, nil, presentation.Pinned))
	}
	return joined
}

func historicalSourceBusyError(err error) bool {
	return errors.Is(err, identitylock.ErrHeld) ||
		errors.Is(err, errHistoricalSourceBusy) ||
		errors.Is(err, agent.ErrSessionLeaseHeld) ||
		errors.Is(err, session.ErrWriterOwned)
}

func (a *App) importHistoricalSource(ctx context.Context, id string, source historicalSource) (SessionRestoreResult, error) {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	if result, handled, err := a.resumeReadyHistoricalImport(ctx, state, id, source); handled {
		return result, err
	}
	release, err := acquireHistoricalSource(ctx, id, source)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	defer release()
	// Another process may have committed between the initial read and our claim.
	state, err = a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	if result, handled, err := a.resumeReadyHistoricalImport(ctx, state, id, source); handled {
		return result, err
	}
	if source.version != "" {
		current, fingerprintErr := desktopSourceFingerprint(source.path)
		if fingerprintErr != nil {
			return SessionRestoreResult{}, fingerprintErr
		}
		if current != source.version {
			return SessionRestoreResult{}, newSessionOperationError("target_changed", "The historical source changed. Check for updates again.")
		}
	}
	workspace, err := a.ensureDesktopWorkspace(ctx, source.scope, source.root)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	if result, handled, err := a.resumeConflictingHistoricalVersion(ctx, state, source, workspace); handled {
		return result, err
	}
	migration := desktopMigrationSource{scope: source.scope, workspaceRoot: source.root, headID: source.head, versionFingerprint: source.version, registeredSourceKey: id}
	if resume := pendingHistoricalOperation(state, id); resume != nil {
		migration.operationID = resume.ID
		migration.registeredSourceKey = resume.Mapping.SourceKey
	}
	err = a.convertHistoricalSource(ctx, source, migration, workspace)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	state, err = a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	mapping, ok, err := state.ResolveSource(id)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	if !ok {
		return SessionRestoreResult{}, errors.New("historical import has not committed")
	}
	if lifecycle := state.SessionStates[mapping.SessionID].Lifecycle; lifecycle != workspacestate.Active {
		return SessionRestoreResult{}, historicalRetiredError(lifecycle)
	}
	return SessionRestoreResult{Session: session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}, WorkspaceID: mapping.WorkspaceID, Generation: state.Generation}, nil
}

func historicalOperationRank(op workspacestate.Operation) int {
	switch op.Phase {
	case "content_ready":
		return 0
	case "prepared":
		return 1
	default:
		return 2
	}
}

func (a *App) convertHistoricalSource(ctx context.Context, source historicalSource, migration desktopMigrationSource, workspace string) (err error) {
	if source.format == "canonical" {
		migration.root = filepath.Dir(source.path)
		old, openErr := session.NewService("migration-source", session.NewFilesystemPersistence(migration.root))
		if openErr != nil {
			return openErr
		}
		defer func() { err = errors.Join(err, old.Shutdown(context.Background())) }()
		return a.migrateCanonicalSession(ctx, old, migration, workspace, filepath.Base(source.path))
	}
	if source.format == "legacy" || source.format == "legacy-trash" {
		return a.migrateLegacySession(ctx, source.path, migration, workspace)
	}
	return errors.New("historical format is unsupported")
}

// StartHistoricalImport snapshots the requested set; later discoveries are not
// silently added. Empty means all currently available/failed/busy sources.
func (a *App) StartHistoricalImport(ids []string) (HistoricalImportStatus, error) {
	_, listErr := a.ListHistoricalSessions()
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	if listErr != nil && len(ids) > 0 {
		for _, id := range ids {
			if _, ok := c.sources[id]; !ok {
				return c.status(), listErr
			}
		}
	}
	if c.running || c.stopped || a.shuttingDown.Load() {
		return c.status(), errors.New("historical import is already running or stopping")
	}
	if err := c.claimQueueLocked(); err != nil {
		return c.status(), err
	}
	started := false
	defer func() {
		if !started {
			c.releaseQueueLocked()
		}
	}()
	if len(ids) == 0 {
		for id, v := range c.views {
			if v.Status != "imported" && v.Status != "deleted" && v.Status != "archived" {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
	}
	seen := map[string]bool{}
	queue := []string{}
	for _, id := range ids {
		if _, ok := c.sources[id]; !ok {
			return c.status(), errors.New("historical source is unavailable")
		}
		if !seen[id] {
			queue = append(queue, id)
			seen[id] = true
		}
	}
	c.queue, c.running, c.paused = queue, true, false
	c.current = ""
	if err := c.saveQueueLocked(); err != nil {
		c.queue, c.running = nil, false
		return c.status(), err
	}
	c.workers.Add(1)
	started = true
	go a.runHistoricalImportQueue()
	return c.status(), nil
}

func (a *App) runHistoricalImportQueue() {
	c := &a.historicalImports
	defer c.workers.Done()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.releaseQueueLocked()
		c.mu.Unlock()
	}()
	for {
		c.mu.Lock()
		if len(c.queue) == 0 || c.stopped || c.ctx.Err() != nil {
			c.mu.Unlock()
			return
		}
		if c.paused {
			wake, ctx := c.wake, c.ctx
			c.mu.Unlock()
			select {
			case <-wake:
			case <-ctx.Done():
			}
			continue
		}
		id := c.queue[0]
		c.queue = c.queue[1:]
		c.current = id
		if err := c.saveQueueLocked(); err != nil {
			c.queue = append([]string{id}, c.queue...)
			c.current, c.paused = "", true
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		call, err := a.prepareHistoricalSession(id, false, true)
		if err == nil {
			_, _ = waitHistoricalImport(call)
		}
		c.mu.Lock()
		// Shutdown preserves the last durable selection, including the current
		// item. A committed item is idempotently resolved on manual continuation.
		if c.stopped || c.ctx.Err() != nil {
			c.paused = true
			c.mu.Unlock()
			return
		}
		if c.current == id {
			c.current = ""
		}
		if err := c.saveQueueLocked(); err != nil {
			c.current, c.paused = id, true
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

// Pause finishes the current item. Cancel also interrupts its source work;
// durable prepared/content_ready records remain available to the next request.
func (a *App) ControlHistoricalImport(action string) (HistoricalImportStatus, error) {
	c := &a.historicalImports
	c.mu.Lock()
	c.initialize(a.bootContext())
	if c.stopped || a.shuttingDown.Load() {
		c.mu.Unlock()
		return HistoricalImportStatus{Items: []HistoricalSessionView{}}, context.Canceled
	}
	if err := c.claimQueueLocked(); err != nil {
		status := c.status()
		c.mu.Unlock()
		return status, err
	}
	startWorker := false
	defer func() {
		c.mu.Lock()
		if !c.running {
			c.releaseQueueLocked()
		}
		c.mu.Unlock()
	}()
	switch action {
	case "pause":
		c.paused = true
	case "resume":
		c.paused = false
		if !c.running && (len(c.queue) > 0 || c.current != "") {
			if c.current != "" {
				c.queue = append([]string{c.current}, c.queue...)
				c.current = ""
			}
			c.running, startWorker = true, true
		}
		select {
		case c.wake <- struct{}{}:
		default:
		}
	case "cancel":
		c.queue = nil
		c.current = ""
		c.paused = false
		for _, call := range c.calls {
			call.batch = false
			if !call.interactive {
				call.cancel()
			}
		}
	default:
		status := c.status()
		c.mu.Unlock()
		return status, errors.New("unknown historical import action")
	}
	if err := c.saveQueueLocked(); err != nil {
		if startWorker {
			c.running = false
		}
		status := c.status()
		c.mu.Unlock()
		return status, err
	}
	status := c.status()
	if startWorker {
		c.workers.Add(1)
	}
	c.mu.Unlock()
	if startWorker {
		go a.runHistoricalImportQueue()
	}
	return status, nil
}

func (a *App) stopHistoricalImports() {
	c := &a.historicalImports
	c.mu.Lock()
	c.initialize(a.bootContext())
	c.stopped = true
	c.cancel()
	for _, call := range c.calls {
		call.cancel()
	}
	c.mu.Unlock()
	// Cancellation and draining happen before the runtime shutdown barrier.
	c.workers.Wait()
}

// Legacy recovery RPCs share cancellation/draining with the on-demand queue.
func (a *App) beginHistoricalRecovery() (context.Context, func(), error) {
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initialize(a.bootContext())
	if c.stopped || a.shuttingDown.Load() {
		return nil, nil, context.Canceled
	}
	c.workers.Add(1)
	return c.ctx, c.workers.Done, nil
}
