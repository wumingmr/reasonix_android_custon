package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/desktop/internal/browserops"
	"reasonix/internal/browser"
	"reasonix/internal/config"
	"reasonix/internal/extension/rpcwire"
)

// Shell error codes for host/browser.* replies; anything else is transport
// failure and therefore an unknown outcome for a reserved write.
const (
	hostBrowserErrStaleReference = -32010
	hostBrowserErrTakenOver      = -32011
	hostBrowserErrNoGrant        = -32012
)

const hostBrowserReadTimeout = 60 * time.Second

type hostRequester interface {
	Request(ctx context.Context, method string, params any, result any) error
}

// hostBrowserExecutor implements browser.Executor for one desktop tab. Every
// call carries the tab's grant; the shell binds tabs, epochs and document
// tokens to that grant so a revoked or restarted service can never act.
type hostBrowserExecutor struct {
	app     *App
	host    hostRequester
	tabID   string
	grantID string
	// sessionKey overrides the grant's session binding when set; remote
	// broker executors use it because their tabs are not workspace tabs.
	sessionKey      string
	diagnosticScope string // Opaque export identity; never used to authorize operations.
	granted         atomic.Bool
	revoked         atomic.Bool
	grantMu         sync.Mutex
}

// tabBrowserExecutor is the stable executor held by a long-lived Controller.
// Every operation follows the runtime-owned sink to its current surface and
// resolves an immutable host grant. Reusing the original surface for another
// session must never give the old controller that session's browser.
type tabBrowserExecutor struct {
	app   *App
	tabID string
	sink  *tabEventSink // runtime-owned binding follows detach/reattach
}

type hostBrowserTab struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Loading   bool   `json:"loading"`
	Temporary bool   `json:"temporary"`
	Error     string `json:"error,omitempty"`
}

func (t hostBrowserTab) tab() browser.Tab {
	return browser.Tab{ID: t.ID, URL: t.URL, Title: t.Title, Loading: t.Loading, Temporary: t.Temporary, Error: t.Error}
}

func (a *App) browserExecutorForTab(tab *WorkspaceTab) browser.Executor {
	if a == nil || tab == nil {
		return nil
	}
	a.mu.RLock()
	tabID, sink := tab.ID, tab.sink
	a.mu.RUnlock()
	return a.browserExecutorForRuntime(tabID, sink)
}

func (a *App) browserExecutorForRuntime(tabID string, sink *tabEventSink) browser.Executor {
	if !a.hostMode() || a.browserControl.off() {
		return nil
	}
	return &tabBrowserExecutor{app: a, tabID: tabID, sink: sink}
}

func (a *App) hostBrowserExecutorForTab(tabID string) *hostBrowserExecutor {
	return a.hostBrowserExecutorForBinding(tabID, nil)
}

// browserBindingTabLocked resolves the runtime's current owner, including a
// detached owner. Holding App.mu before reading the sink binding matches the
// transfer lock order and prevents a stale tab ID from selecting another task.
func (a *App) browserBindingTabLocked(tabID string, sink *tabEventSink) *WorkspaceTab {
	if sink != nil {
		tabID, _ = sink.binding()
	}
	tab := a.tabByEventSinkIDLocked(tabID)
	if tab == nil || tab.removed || (sink != nil && tab.sink != sink) {
		return nil
	}
	return tab
}

func (a *App) hostBrowserExecutorForBinding(tabID string, sink *tabEventSink) *hostBrowserExecutor {
	a.mu.RLock()
	tab := a.browserBindingTabLocked(tabID, sink)
	if tab == nil {
		a.mu.RUnlock()
		return nil
	}
	tabID = tab.ID
	identity := tab.SessionID
	if identity == "" {
		identity = tab.SessionPath
	}
	sessionKey := fmt.Sprintf("%s:%d", identity, tab.SessionGeneration)
	a.browserExecMu.Lock()
	if a.browserExecutors == nil {
		a.browserExecutors = map[string]*hostBrowserExecutor{}
	}
	if exec, ok := a.browserExecutors[tabID]; ok {
		if exec.sessionKey == sessionKey {
			a.browserExecMu.Unlock()
			a.mu.RUnlock()
			return exec
		}
		delete(a.browserExecutors, tabID)
		replacement := &hostBrowserExecutor{app: a, host: a.hostShell.server, tabID: tabID, grantID: newBrowserGrantID(), sessionKey: sessionKey, diagnosticScope: browserDiagnosticScope(localDesktopHostID, tab.SessionID)}
		a.browserExecutors[tabID] = replacement
		a.browserExecMu.Unlock()
		a.mu.RUnlock()
		a.releaseFileBrowserPreviewsForTask(tabID)
		a.revokeBrowserExecutor(exec)
		return replacement
	}
	exec := &hostBrowserExecutor{app: a, host: a.hostShell.server, tabID: tabID, grantID: newBrowserGrantID(), sessionKey: sessionKey, diagnosticScope: browserDiagnosticScope(localDesktopHostID, tab.SessionID)}
	a.browserExecutors[tabID] = exec
	a.browserExecMu.Unlock()
	a.mu.RUnlock()
	return exec
}

func newBrowserGrantID() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "grant-" + hex.EncodeToString(buf)
}

func (a *App) revokeRemoteBrowserHost(hostID string) {
	a.remoteTabMu.Lock()
	var ids []string
	for _, tab := range a.remoteTabs {
		if tab != nil && tab.ref.HostID == hostID {
			ids = append(ids, tab.id)
		}
	}
	a.remoteTabMu.Unlock()
	for _, id := range ids {
		a.forgetRemoteBrowserExecutor(id)
	}
}

// forgetBrowserExecutorLocked drops the tab's executor and revokes its grant
// off the caller's lock; a revoked executor fails closed forever.
func (a *App) forgetBrowserExecutorLocked(tabID string) {
	a.browserExecMu.Lock()
	exec, ok := a.browserExecutors[tabID]
	delete(a.browserExecutors, tabID)
	a.browserExecMu.Unlock()
	a.releaseFileBrowserPreviewsForTask(tabID)
	if !ok {
		return
	}
	a.revokeBrowserExecutor(exec)
}

func (e *tabBrowserExecutor) current() (*hostBrowserExecutor, error) {
	if e == nil || e.app == nil || !e.app.hostMode() || e.app.browserControl.off() {
		return nil, browser.ErrNoGrant
	}
	exec := e.app.hostBrowserExecutorForBinding(e.tabID, e.sink)
	if exec == nil {
		return nil, browser.ErrNoGrant
	}
	return exec, nil
}

func (e *tabBrowserExecutor) Available(ctx context.Context) bool {
	return e.UnavailableReason(ctx) == ""
}

// Discovery is observational: do not mint or rotate a grant while searching
// the capability catalog. Execution resolves the same binding under App.mu.
func (e *tabBrowserExecutor) UnavailableReason(context.Context) string {
	if e == nil || e.app == nil || !e.app.hostMode() {
		return "the built-in browser host is not attached to this task"
	}
	if e.app.browserControl.off() {
		return "built-in browser control is disabled in the host settings"
	}
	e.app.mu.RLock()
	tab := e.app.browserBindingTabLocked(e.tabID, e.sink)
	e.app.mu.RUnlock()
	if tab == nil {
		return "the browser's task runtime binding is no longer available"
	}
	return ""
}
func (e *tabBrowserExecutor) Tabs(ctx context.Context) ([]browser.Tab, error) {
	x, err := e.current()
	if err != nil {
		return nil, err
	}
	return x.Tabs(ctx)
}
func (e *tabBrowserExecutor) Open(ctx context.Context, req browser.OpenRequest) (browser.Tab, error) {
	x, err := e.current()
	if err != nil {
		return browser.Tab{}, err
	}
	return x.Open(ctx, req)
}
func (e *tabBrowserExecutor) Navigate(ctx context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	x, err := e.current()
	if err != nil {
		return browser.Tab{}, err
	}
	return x.Navigate(ctx, req)
}
func (e *tabBrowserExecutor) Close(ctx context.Context, req browser.CloseRequest) error {
	x, err := e.current()
	if err != nil {
		return err
	}
	return x.Close(ctx, req)
}
func (e *tabBrowserExecutor) Snapshot(ctx context.Context, req browser.SnapshotRequest) (browser.Snapshot, error) {
	x, err := e.current()
	if err != nil {
		return browser.Snapshot{}, err
	}
	return x.Snapshot(ctx, req)
}
func (e *tabBrowserExecutor) Screenshot(ctx context.Context, req browser.ScreenshotRequest) (browser.Screenshot, error) {
	x, err := e.current()
	if err != nil {
		return browser.Screenshot{}, err
	}
	return x.Screenshot(ctx, req)
}
func (e *tabBrowserExecutor) Act(ctx context.Context, req browser.ActRequest) (browser.ActResult, error) {
	x, err := e.current()
	if err != nil {
		return browser.ActResult{}, err
	}
	return x.Act(ctx, req)
}
func (e *tabBrowserExecutor) Downloads(ctx context.Context, req browser.DownloadsRequest) ([]browser.Download, error) {
	x, err := e.current()
	if err != nil {
		return nil, err
	}
	return x.Downloads(ctx, req)
}
func (e *tabBrowserExecutor) PreviewFile(ctx context.Context, req browser.FilePreviewRequest) (browser.Tab, error) {
	x, err := e.current()
	if err != nil {
		return browser.Tab{}, err
	}
	return x.PreviewFile(ctx, req)
}

func (a *App) revokeBrowserExecutor(exec *hostBrowserExecutor) {
	exec.revoked.Store(true)
	a.goSafe("revokeBrowserGrant", func() {
		exec.grantMu.Lock()
		defer exec.grantMu.Unlock()
		if !exec.granted.Load() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), rpcHostWindowTimeout)
		defer cancel()
		_ = exec.host.Request(ctx, "host/browser.revoke", map[string]string{"grantId": exec.grantID}, nil)
	})
}

// forgetRemoteBrowserExecutor drops the broker executor of a closed remote
// tab; its grant ID is namespaced with the "remote/" prefix used at creation.
func (a *App) forgetRemoteBrowserExecutor(remoteTabID string) {
	a.forgetBrowserExecutorLocked("remote/" + remoteTabID)
}

func (a *App) browserLedger() (*browserops.Ledger, error) {
	a.browserExecMu.Lock()
	defer a.browserExecMu.Unlock()
	if a.browserOps != nil {
		return a.browserOps, nil
	}
	ledger, err := browserops.Open(filepath.Join(config.MemoryUserDir(), "browser", "operations-v1.json"))
	if err != nil {
		return nil, err
	}
	a.browserOps = ledger
	return ledger, nil
}

func (e *hostBrowserExecutor) Available(context.Context) bool {
	return !e.revoked.Load() && e.app.hostMode()
}

// browserSessionKey is the session identity the grant binds to: the explicit
// override for broker-created executors, else the workspace tab's session.
func (e *hostBrowserExecutor) browserSessionKey() string {
	if e.sessionKey != "" {
		return e.sessionKey
	}
	return e.app.tabSessionKeyForBrowser(e.tabID)
}

func (e *hostBrowserExecutor) ensureGrant(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.revoked.Load() {
		return browser.ErrNoGrant
	}
	if e.granted.Load() {
		return nil
	}
	e.grantMu.Lock()
	defer e.grantMu.Unlock()
	if e.revoked.Load() {
		return browser.ErrNoGrant
	}
	if e.granted.Load() {
		return nil
	}
	params := map[string]string{"grantId": e.grantID, "tabId": e.tabID, "sessionId": e.browserSessionKey()}
	params["diagnosticScope"] = e.diagnosticScope
	if err := e.host.Request(ctx, "host/browser.grant", params, nil); err != nil {
		return mapHostBrowserError(err)
	}
	if e.revoked.Load() {
		return browser.ErrNoGrant
	}
	e.granted.Store(true)
	return nil
}

func (a *App) tabSessionKeyForBrowser(tabID string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if tab, ok := a.tabs[tabID]; ok {
		return tab.SessionPath
	}
	return ""
}

func (e *hostBrowserExecutor) call(ctx context.Context, method string, params map[string]any, result any) error {
	if err := e.ensureGrant(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if params == nil {
		params = map[string]any{}
	}
	params["grantId"] = e.grantID
	ctx, cancel := context.WithTimeout(ctx, hostBrowserReadTimeout)
	defer cancel()
	requestID := make([]byte, 16)
	if _, err := rand.Read(requestID); err != nil {
		return err
	}
	params["requestId"] = hex.EncodeToString(requestID)
	if deadline, ok := ctx.Deadline(); ok {
		params["deadline"] = deadline.UnixMilli()
	}
	if err := e.host.Request(ctx, method, params, result); err != nil {
		if ctx.Err() != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
			defer cleanupCancel()
			// Old shells may not implement cancellation; preserve the original
			// unknown write outcome rather than retrying the operation.
			_ = e.host.Request(cleanupCtx, "host/browser.cancel", map[string]any{"grantId": e.grantID, "requestId": params["requestId"]}, nil)
		}
		return mapHostBrowserError(err)
	}
	return nil
}

func mapHostBrowserError(err error) error {
	var resp *rpcwire.ResponseError
	if errors.As(err, &resp) {
		switch resp.Code {
		case hostBrowserErrStaleReference:
			return browser.ErrStaleReference
		case hostBrowserErrTakenOver:
			return browser.ErrTakenOver
		case hostBrowserErrNoGrant:
			return browser.ErrNoGrant
		}
		return fmt.Errorf("browser host: %s", resp.Message)
	}
	return err
}

func (e *hostBrowserExecutor) Tabs(ctx context.Context) ([]browser.Tab, error) {
	var out struct {
		Tabs []hostBrowserTab `json:"tabs"`
	}
	if err := e.call(ctx, "host/browser.tabs.list", nil, &out); err != nil {
		return nil, err
	}
	tabs := make([]browser.Tab, 0, len(out.Tabs))
	for _, t := range out.Tabs {
		tabs = append(tabs, t.tab())
	}
	return tabs, nil
}

func (e *hostBrowserExecutor) Open(ctx context.Context, req browser.OpenRequest) (browser.Tab, error) {
	var out hostBrowserTab
	err := e.write(ctx, req.OperationID, "open", "", req, "host/browser.tabs.open", map[string]any{"url": req.URL, "temporary": req.Temporary}, &out)
	return out.tab(), err
}

func (e *hostBrowserExecutor) Navigate(ctx context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	var out hostBrowserTab
	err := e.write(ctx, req.OperationID, "navigate", req.TabID, req, "host/browser.tabs.navigate", map[string]any{"tabId": req.TabID, "url": req.URL, "action": req.Action}, &out)
	return out.tab(), err
}

// navigateFilePreview is renderer-only plumbing for an explicit refresh. The
// host still verifies task and session ownership, but does not require agent
// mode, so refreshing a user-taken-over page does not hand control back.
func (e *hostBrowserExecutor) navigateFilePreview(ctx context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	var out hostBrowserTab
	err := e.write(ctx, req.OperationID, "navigate", req.TabID, req, "host/browser.tabs.navigate", map[string]any{
		"tabId": req.TabID, "url": req.URL, "action": req.Action, "allowHuman": true,
	}, &out)
	return out.tab(), err
}

func (e *hostBrowserExecutor) Close(ctx context.Context, req browser.CloseRequest) error {
	err := e.write(ctx, req.OperationID, "close", req.TabID, req, "host/browser.tabs.close", map[string]any{"tabId": req.TabID}, nil)
	if err == nil {
		e.app.releaseFileBrowserPreviewTab(req.TabID)
	}
	return err
}

func (e *hostBrowserExecutor) PreviewFile(ctx context.Context, req browser.FilePreviewRequest) (browser.Tab, error) {
	_, generation, err := e.app.fileBrowserPreviewTab(e.tabID, 0)
	if err != nil {
		return browser.Tab{}, err
	}
	result, err := e.app.openFileBrowserPreview(ctx, e.tabID, FileBrowserPreviewRequest{
		Source: req.Source, Path: req.Path, ToolCallID: req.ToolCallID, OperationID: req.OperationID,
		ExpectedSessionGeneration: generation,
	}, e)
	if err != nil {
		return browser.Tab{}, err
	}
	if result.Error != "" {
		return browser.Tab{}, errors.New(result.Error)
	}
	return browser.Tab{ID: result.TabID, URL: result.URL, Loading: result.Status == "loading"}, nil
}

// Every browser write uses the same durable reservation, including history
// operations whose reply may disappear after the browser already navigated.
func (e *hostBrowserExecutor) write(ctx context.Context, id, action, tabID string, request any, method string, params map[string]any, out any) error {
	if err := e.ensureGrant(ctx); err != nil {
		return err
	}
	ledger, err := e.app.browserLedger()
	if err != nil {
		return err
	}
	digest, err := actDigest(request)
	if err != nil {
		return err
	}
	if err := ledger.Reserve(browserops.Operation{ID: id, SessionID: e.browserSessionKey(), Generation: e.grantID, TabID: tabID, Action: action, Digest: digest, DiagnosticScope: e.diagnosticScope}); err != nil {
		if errors.Is(err, browserops.ErrDuplicateOperation) {
			return fmt.Errorf("%w: operationId already recorded", browser.ErrUnknownOutcome)
		}
		return err
	}
	if params == nil {
		params = map[string]any{}
	}
	params["operationId"] = id
	err = e.call(ctx, method, params, out)
	if err == nil {
		if raw, ok := out.(*json.RawMessage); ok {
			var receipt struct {
				Outcome string `json:"outcome"`
			}
			if json.Unmarshal(*raw, &receipt) == nil && receipt.Outcome == "unknown" {
				e.settle(ledger, id, browserops.StateUnknown, "host reported an interrupted operation")
				return browser.ErrUnknownOutcome
			}
		}
		e.settle(ledger, id, browserops.StateExecuted, "")
		return nil
	}
	if errors.Is(err, browser.ErrNoGrant) || errors.Is(err, browser.ErrTakenOver) || errors.Is(err, browser.ErrStaleReference) {
		e.settle(ledger, id, browserops.StateNotExecuted, err.Error())
		return err
	}
	e.settle(ledger, id, browserops.StateUnknown, err.Error())
	return fmt.Errorf("%w: %s", browser.ErrUnknownOutcome, err.Error())
}

func (e *hostBrowserExecutor) Snapshot(ctx context.Context, req browser.SnapshotRequest) (browser.Snapshot, error) {
	var out struct {
		Observation   *browser.Observation `json:"observation"`
		DocumentToken string               `json:"documentToken"`
		URL           string               `json:"url"`
		Title         string               `json:"title"`
		Tree          string               `json:"tree"`
		Refs          int                  `json:"refs"`
	}
	err := e.call(ctx, "host/browser.snapshot", map[string]any{"tabId": req.TabID, "selector": req.Selector}, &out)
	return browser.Snapshot{DocumentToken: out.DocumentToken, URL: out.URL, Title: out.Title, Tree: out.Tree, Refs: out.Refs, Observation: out.Observation}, err
}

func (e *hostBrowserExecutor) Screenshot(ctx context.Context, req browser.ScreenshotRequest) (browser.Screenshot, error) {
	dir, err := e.captureDir()
	if err != nil {
		return browser.Screenshot{}, err
	}
	var out struct {
		Observation      *browser.Observation `json:"observation"`
		Path             string               `json:"path"`
		MIME             string               `json:"mime"`
		Width            int                  `json:"width"`
		Height           int                  `json:"height"`
		ObservationToken string               `json:"observationToken"`
		CSSWidth         int                  `json:"cssWidth"`
		CSSHeight        int                  `json:"cssHeight"`
	}
	err = e.call(ctx, "host/browser.screenshot", map[string]any{"tabId": req.TabID, "ref": req.Ref, "fullPage": req.FullPage, "directory": dir}, &out)
	return browser.Screenshot{Path: out.Path, MIME: out.MIME, Width: out.Width, Height: out.Height, ObservationToken: out.ObservationToken, CSSWidth: out.CSSWidth, CSSHeight: out.CSSHeight, Observation: out.Observation}, err
}

// captureDir is the task-owned scratch directory the shell writes captures
// and downloads into; it lives outside the data home and is per tab.
func (e *hostBrowserExecutor) captureDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "reasonix-browser", e.tabID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func (e *hostBrowserExecutor) Downloads(ctx context.Context, req browser.DownloadsRequest) ([]browser.Download, error) {
	var out struct {
		Downloads []struct {
			ID    string `json:"id"`
			URL   string `json:"url"`
			Path  string `json:"path"`
			State string `json:"state"`
			Bytes int64  `json:"bytes"`
		} `json:"downloads"`
	}
	params := map[string]any{"tabId": req.TabID, "waitForMs": req.WaitFor.Milliseconds()}
	if err := e.call(ctx, "host/browser.downloads", params, &out); err != nil {
		return nil, err
	}
	downloads := make([]browser.Download, 0, len(out.Downloads))
	for _, d := range out.Downloads {
		downloads = append(downloads, browser.Download{ID: d.ID, URL: d.URL, Path: d.Path, State: d.State, Bytes: d.Bytes})
	}
	return downloads, nil
}

// Act reserves the operation in the ledger before the shell touches the
// page and settles it from the receipt. A lost receipt stays unknown and is
// reported as such; the ledger rejects the same operationId forever.
func (e *hostBrowserExecutor) Act(ctx context.Context, req browser.ActRequest) (browser.ActResult, error) {
	ledger, err := e.app.browserLedger()
	if err != nil {
		return browser.ActResult{}, err
	}
	if err := e.ensureGrant(ctx); err != nil {
		return browser.ActResult{}, err
	}
	digest, err := actDigest(req)
	if err != nil {
		return browser.ActResult{}, err
	}
	op := browserops.Operation{
		DiagnosticScope: e.diagnosticScope,
		ID:              req.OperationID,
		SessionID:       e.browserSessionKey(),
		Generation:      e.grantID,
		TabID:           req.TabID,
		DocumentToken:   req.DocumentToken,
		Action:          req.Action,
		Digest:          digest,
	}
	if err := ledger.Reserve(op); err != nil {
		if errors.Is(err, browserops.ErrDuplicateOperation) {
			return browser.ActResult{Outcome: browser.OutcomeUnknown}, fmt.Errorf("%w: operationId already recorded", browser.ErrUnknownOutcome)
		}
		return browser.ActResult{}, err
	}
	if req.Action == browser.ActionUpload {
		files, cleanup, err := e.prepareUploadFiles(req.Files)
		if err != nil {
			e.settle(ledger, req.OperationID, browserops.StateNotExecuted, err.Error())
			return browser.ActResult{}, err
		}
		defer cleanup()
		req.Files = files
	}
	var out struct {
		Executed      *bool  `json:"executed"`
		Outcome       string `json:"outcome"`
		Reason        string `json:"reason"`
		DocumentToken string `json:"documentToken"`
	}
	params := map[string]any{
		"operationId": req.OperationID, "tabId": req.TabID, "documentToken": req.DocumentToken,
		"action": req.Action, "ref": req.Ref, "text": req.Text, "keys": req.Keys,
		"options": nonNil(req.Options), "files": nonNil(req.Files), "submit": req.Submit,
		"deltaX": req.DeltaX, "deltaY": req.DeltaY,
	}
	callErr := e.call(ctx, "host/browser.act", params, &out)
	switch {
	case callErr == nil && out.Executed == nil:
		e.settle(ledger, req.OperationID, browserops.StateUnknown, "host returned no execution receipt")
		return browser.ActResult{Outcome: browser.OutcomeUnknown}, browser.ErrUnknownOutcome
	case callErr == nil && out.Outcome == browser.OutcomeUnknown:
		e.settle(ledger, req.OperationID, browserops.StateUnknown, out.Reason)
		return browser.ActResult{Outcome: browser.OutcomeUnknown}, fmt.Errorf("%w: %s", browser.ErrUnknownOutcome, out.Reason)
	case callErr == nil && *out.Executed:
		e.settle(ledger, req.OperationID, browserops.StateExecuted, "")
		return browser.ActResult{Executed: true, Outcome: browser.OutcomeExecuted, DocumentToken: out.DocumentToken}, nil
	case callErr == nil:
		e.settle(ledger, req.OperationID, browserops.StateNotExecuted, out.Reason)
		return browser.ActResult{Executed: false, Outcome: browser.OutcomeNotExecuted, Reason: out.Reason, DocumentToken: out.DocumentToken}, nil
	case errors.Is(callErr, browser.ErrStaleReference), errors.Is(callErr, browser.ErrTakenOver), errors.Is(callErr, browser.ErrNoGrant):
		e.settle(ledger, req.OperationID, browserops.StateNotExecuted, callErr.Error())
		return browser.ActResult{}, callErr
	default:
		e.settle(ledger, req.OperationID, browserops.StateUnknown, callErr.Error())
		return browser.ActResult{Outcome: browser.OutcomeUnknown}, fmt.Errorf("%w: %s", browser.ErrUnknownOutcome, callErr.Error())
	}
}

func (e *hostBrowserExecutor) settle(ledger *browserops.Ledger, id string, state browserops.State, reason string) {
	if err := ledger.Settle(id, state, reason); err != nil {
		slog.Warn("desktop browser: settle operation", "operation", id, "state", state, "err", err)
	}
}

func actDigest(req any) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
