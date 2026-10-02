package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"reasonix/internal/browser"
)

const (
	defaultNavigateTimeout = 30 * time.Second
	healthTTL              = 30 * time.Second
	// maxOperations bounds the single-use ledger. The ceiling exists so a
	// runaway loop cannot grow it without limit; no real session approaches it.
	maxOperations = 50000
)

// Options configures one CDP-backed executor.
type Options struct {
	// Endpoint is an existing DevTools endpoint such as http://127.0.0.1:9222
	// or a ws:// socket URL. Empty launches a browser this executor owns.
	Endpoint string
	// AllowRemoteEndpoint permits a non-loopback Endpoint. A DevTools endpoint
	// grants full control of the browser, so this stays off by default.
	AllowRemoteEndpoint bool
	ChromePath          string
	ChromeArgs          []string
	// UserDataDir is the launched browser's profile. Empty uses a temporary
	// directory that is removed on Shutdown.
	UserDataDir     string
	Headless        bool
	LaunchTimeout   time.Duration
	NavigateTimeout time.Duration
	// ArtifactDir receives screenshots and downloads. Empty uses a temporary
	// directory that is removed on Shutdown.
	ArtifactDir string
	// UploadRoots are the directories browser_upload may read from, beside the
	// artifact directory. Empty leaves only the artifact directory, because a
	// file input on an untrusted page must never reach the whole filesystem.
	UploadRoots []string
	HTTPClient  *http.Client
}

// Executor drives an external Chrome and satisfies browser.Executor. It owns
// the operation ledger and document tokens that a raw browser has no notion of.
type Executor struct {
	opts      Options
	conn      *conn
	proc      *launched
	artifacts string
	ownedDir  bool
	navWait   time.Duration
	uploads   uploadRoots

	mu        sync.Mutex
	pages     map[string]*page
	ops       map[string]string
	contexts  map[string]int
	downloads map[string]*downloadRecord
	nextTab   int
	closed    bool
	healthyAt time.Time
	now       func() time.Time
}

var _ browser.Executor = (*Executor)(nil)

// New attaches to the configured DevTools endpoint, or launches a browser when
// none is configured, and returns the executor that drives it.
func New(ctx context.Context, opts Options) (*Executor, error) {
	artifacts, ownedDir, err := artifactDir(opts.ArtifactDir)
	if err != nil {
		return nil, err
	}
	e := &Executor{
		opts: opts, artifacts: artifacts, ownedDir: ownedDir,
		navWait:   cmpDuration(opts.NavigateTimeout, defaultNavigateTimeout),
		uploads:   newUploadRoots(opts.UploadRoots, artifacts),
		pages:     map[string]*page{},
		ops:       map[string]string{},
		contexts:  map[string]int{},
		downloads: map[string]*downloadRecord{},
		now:       time.Now,
	}
	wsURL, err := e.endpoint(ctx)
	if err != nil {
		e.cleanup()
		return nil, err
	}
	if e.conn, err = dialConn(ctx, wsURL); err != nil {
		e.cleanup()
		return nil, err
	}
	e.watch()
	if err := e.setDownloadBehavior(ctx, ""); err != nil {
		e.Shutdown()
		return nil, err
	}
	return e, nil
}

// endpoint resolves the socket to drive, launching a browser when the caller
// configured no endpoint of their own.
func (e *Executor) endpoint(ctx context.Context) (string, error) {
	if e.opts.Endpoint == "" {
		proc, wsURL, err := launchChrome(ctx, e.opts)
		if err != nil {
			return "", err
		}
		e.proc = proc
		return wsURL, nil
	}
	launchCtx, cancel := context.WithTimeout(ctx, cmpDuration(e.opts.LaunchTimeout, 10*time.Second))
	defer cancel()
	return waitForEndpoint(launchCtx, e.opts.Endpoint, e.opts.HTTPClient, e.opts.AllowRemoteEndpoint)
}

// watch binds browser-level events: a detached target is a tab that is gone,
// and download events feed the per-tab download list.
func (e *Executor) watch() {
	e.conn.on("", "Target.detachedFromTarget", func(params json.RawMessage) {
		var ev struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(params, &ev); err != nil {
			return
		}
		if p := e.pageBySession(ev.SessionID); p != nil {
			p.markDead()
		}
	})
	e.conn.on("", "Browser.downloadWillBegin", e.downloadWillBegin)
	e.conn.on("", "Browser.downloadProgress", e.downloadProgress)
}

// Shutdown releases the browser: a launched one is killed, an attached one
// keeps running with the tabs this executor opened closed behind it.
func (e *Executor) Shutdown() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	pages := make([]*page, 0, len(e.pages))
	for _, p := range e.pages {
		pages = append(pages, p)
	}
	e.pages = map[string]*page{}
	e.mu.Unlock()

	if e.conn != nil {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for _, p := range pages {
			p.unsubscribe()
			_ = e.conn.call(shutdown, "", "Target.closeTarget", map[string]any{"targetId": p.target}, nil)
		}
		cancel()
		e.conn.close()
	}
	e.cleanup()
}

func (e *Executor) cleanup() {
	if e.proc != nil {
		e.proc.stop()
	}
	if e.ownedDir && e.artifacts != "" {
		_ = os.RemoveAll(e.artifacts)
	}
}

// Available reports whether the browser still answers. The probe is cached so
// a tool surface that asks per turn does not round-trip every time.
func (e *Executor) Available(ctx context.Context) bool {
	e.mu.Lock()
	closed, fresh := e.closed, !e.healthyAt.IsZero() && e.now().Sub(e.healthyAt) < healthTTL
	e.mu.Unlock()
	if closed || e.conn == nil || e.conn.closed() {
		return false
	}
	if fresh {
		return true
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := e.conn.call(probe, "", "Browser.getVersion", nil, nil); err != nil {
		return false
	}
	e.mu.Lock()
	e.healthyAt = e.now()
	e.mu.Unlock()
	return true
}

// reserve records a model-minted operationId. Reuse is refused forever: a
// replayed id means the model is retrying a write it was told not to retry, and
// the earlier attempt's effect — including an unknown one — already stands.
func (e *Executor) reserve(id, what string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return fmt.Errorf("the browser is closed")
	}
	if prev, ok := e.ops[id]; ok {
		return fmt.Errorf("operationId %q was already used for %s; that attempt's outcome stands. Take a browser_snapshot to see the page before deciding what to do next", id, prev)
	}
	if len(e.ops) >= maxOperations {
		return fmt.Errorf("this session has run %d browser writes, the ceiling for one browser", maxOperations)
	}
	e.ops[id] = what
	return nil
}

// lookup resolves a tab the calling session owns.
func (e *Executor) lookup(ctx context.Context, tabID string) (*page, error) {
	e.mu.Lock()
	p, ok := e.pages[tabID]
	closed := e.closed
	e.mu.Unlock()
	switch {
	case closed:
		return nil, browser.ErrNoGrant
	case !ok:
		return nil, fmt.Errorf("no tab %q is open for this task; call browser_tabs to list them", tabID)
	case p.owner != browser.SessionFromContext(ctx):
		return nil, browser.ErrNoGrant
	case p.isDead():
		return nil, fmt.Errorf("tab %s is gone: the page crashed or the browser closed it", tabID)
	}
	return p, nil
}

func (e *Executor) pageBySession(sessionID string) *page {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.pages {
		if p.session == sessionID {
			return p
		}
	}
	return nil
}

// Tabs lists the calling session's tabs, refreshed from the browser so a page
// that navigated itself reports its current address.
func (e *Executor) Tabs(ctx context.Context) ([]browser.Tab, error) {
	owner := browser.SessionFromContext(ctx)
	e.mu.Lock()
	owned := make([]*page, 0, len(e.pages))
	for _, p := range e.pages {
		if p.owner == owner && !p.isDead() {
			owned = append(owned, p)
		}
	}
	e.mu.Unlock()
	tabs := make([]browser.Tab, 0, len(owned))
	for _, p := range owned {
		e.refreshTarget(ctx, p)
		tabs = append(tabs, p.tab())
	}
	sortTabs(tabs)
	return tabs, nil
}

// refreshTarget reads URL and title from the browser rather than the page, so
// listing tabs never runs script in a document the agent has not snapshotted.
func (e *Executor) refreshTarget(ctx context.Context, p *page) {
	var info struct {
		TargetInfo struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"targetInfo"`
	}
	if err := e.conn.call(ctx, "", "Target.getTargetInfo", map[string]any{"targetId": p.target}, &info); err != nil {
		return
	}
	p.mu.Lock()
	p.url, p.title = info.TargetInfo.URL, info.TargetInfo.Title
	p.mu.Unlock()
}

// Open creates a tab for the calling session. A temporary tab gets its own
// browser context, which shares no cookies and is discarded with the tab.
func (e *Executor) Open(ctx context.Context, req browser.OpenRequest) (browser.Tab, error) {
	if err := e.reserve(req.OperationID, "browser_open "+req.URL); err != nil {
		return browser.Tab{}, err
	}
	contextID, err := e.openContext(ctx, req.Temporary)
	if err != nil {
		return browser.Tab{}, err
	}
	params := map[string]any{"url": "about:blank"}
	if contextID != "" {
		params["browserContextId"] = contextID
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := e.conn.call(ctx, "", "Target.createTarget", params, &created); err != nil {
		e.releaseContext(ctx, contextID)
		return browser.Tab{}, fmt.Errorf("open tab: %w", err)
	}
	p, err := e.register(ctx, created.TargetID, contextID, req.Temporary)
	if err != nil {
		_ = e.conn.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": created.TargetID}, nil)
		e.releaseContext(ctx, contextID)
		return browser.Tab{}, err
	}
	if err := e.navigate(ctx, p, req.URL); err != nil {
		return p.tab(), err
	}
	return p.tab(), nil
}

// register attaches to a freshly created target and gives it a tab ID.
func (e *Executor) register(ctx context.Context, target, contextID string, temporary bool) (*page, error) {
	e.mu.Lock()
	e.nextTab++
	id := "tab-" + strconv.Itoa(e.nextTab)
	owner := browser.SessionFromContext(ctx)
	e.mu.Unlock()

	p, err := e.attach(ctx, id, target, contextID, owner, temporary)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.pages[id] = p
	if contextID != "" {
		e.contexts[contextID]++
	}
	e.mu.Unlock()
	return p, nil
}

func (e *Executor) openContext(ctx context.Context, temporary bool) (string, error) {
	if !temporary {
		return "", nil
	}
	var out struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := e.conn.call(ctx, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &out); err != nil {
		return "", fmt.Errorf("create temporary partition: %w", err)
	}
	if err := e.setDownloadBehavior(ctx, out.BrowserContextID); err != nil {
		return "", err
	}
	return out.BrowserContextID, nil
}

// releaseContext disposes a temporary partition once its last tab is gone.
func (e *Executor) releaseContext(ctx context.Context, contextID string) {
	if contextID == "" {
		return
	}
	e.mu.Lock()
	e.contexts[contextID]--
	remaining := e.contexts[contextID]
	if remaining <= 0 {
		delete(e.contexts, contextID)
	}
	e.mu.Unlock()
	if remaining > 0 {
		return
	}
	_ = e.conn.call(ctx, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": contextID}, nil)
}

// Navigate moves a tab and retires every ref and token bound to the document
// it is leaving.
func (e *Executor) Navigate(ctx context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	p, err := e.lookup(ctx, req.TabID)
	if err != nil {
		return browser.Tab{}, err
	}
	if err := e.reserve(req.OperationID, "browser_navigate "+req.Action+" on "+req.TabID); err != nil {
		return browser.Tab{}, err
	}
	p.retire("")
	switch req.Action {
	case browser.NavigateURL:
		err = e.navigate(ctx, p, req.URL)
	case browser.NavigateReload:
		err = e.withLoad(ctx, p, func(loadCtx context.Context) error {
			return e.conn.call(loadCtx, p.session, "Page.reload", map[string]any{}, nil)
		})
	case browser.NavigateBack, browser.NavigateForward:
		err = e.history(ctx, p, req.Action)
	default:
		return browser.Tab{}, fmt.Errorf("unknown navigate action %q", req.Action)
	}
	if err != nil {
		return p.tab(), err
	}
	e.refreshTarget(ctx, p)
	return p.tab(), nil
}

func (e *Executor) navigate(ctx context.Context, p *page, url string) error {
	err := e.withLoad(ctx, p, func(loadCtx context.Context) error {
		var out struct {
			ErrorText string `json:"errorText"`
		}
		if err := e.conn.call(loadCtx, p.session, "Page.navigate", map[string]any{"url": url}, &out); err != nil {
			return err
		}
		if out.ErrorText != "" {
			return fmt.Errorf("navigate to %s: %s", url, out.ErrorText)
		}
		return nil
	})
	if err != nil {
		return err
	}
	e.refreshTarget(ctx, p)
	return nil
}

// history walks the tab's navigation history one entry in either direction.
func (e *Executor) history(ctx context.Context, p *page, action string) error {
	var hist struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := e.conn.call(ctx, p.session, "Page.getNavigationHistory", nil, &hist); err != nil {
		return err
	}
	index := hist.CurrentIndex - 1
	if action == browser.NavigateForward {
		index = hist.CurrentIndex + 1
	}
	if index < 0 || index >= len(hist.Entries) {
		return fmt.Errorf("the tab has no %s entry in its history", action)
	}
	entry := hist.Entries[index].ID
	return e.withLoad(ctx, p, func(loadCtx context.Context) error {
		return e.conn.call(loadCtx, p.session, "Page.navigateToHistoryEntry", map[string]any{"entryId": entry}, nil)
	})
}

// withLoad runs a navigation command and waits for the page to settle. A
// timeout is not an error: the tools let the model snapshot a slow page.
func (e *Executor) withLoad(ctx context.Context, p *page, run func(context.Context) error) error {
	loaded, cancel := e.conn.once(p.session, "Page.frameStoppedLoading")
	defer cancel()
	p.setLoading(true)
	if err := run(ctx); err != nil {
		p.setLoading(false)
		return err
	}
	wait, stop := context.WithTimeout(ctx, e.navWait)
	defer stop()
	select {
	case <-loaded:
		p.setLoading(false)
	case <-wait.Done():
	}
	return nil
}

// Close closes one tab and disposes a temporary partition with its last tab.
func (e *Executor) Close(ctx context.Context, req browser.CloseRequest) error {
	p, err := e.lookup(ctx, req.TabID)
	if err != nil {
		return err
	}
	if err := e.reserve(req.OperationID, "browser_close "+req.TabID); err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.pages, req.TabID)
	e.mu.Unlock()
	p.unsubscribe()
	if err := e.conn.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": p.target}, nil); err != nil {
		return fmt.Errorf("close tab %s: %w", req.TabID, err)
	}
	e.releaseContext(ctx, p.context)
	return nil
}

func artifactDir(configured string) (string, bool, error) {
	if dir := configured; dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", false, fmt.Errorf("cdp: artifact dir %s: %w", dir, err)
		}
		return dir, false, nil
	}
	dir, err := os.MkdirTemp("", "reasonix-browser-")
	if err != nil {
		return "", false, fmt.Errorf("cdp: artifact dir: %w", err)
	}
	return dir, true, nil
}

// artifactPath names a file inside the executor's own directory. A name is a
// leaf, never a path: nothing this package writes may be steered out of the
// directory the session cleans up.
func (e *Executor) artifactPath(kind, name string) (string, error) {
	if name != "" && (name != filepath.Base(name) || strings.ContainsAny(name, `/\`)) {
		return "", fmt.Errorf("%s name %q is not a plain file name", kind, name)
	}
	dir := filepath.Join(e.artifacts, kind)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("prepare %s directory: %w", kind, err)
	}
	return filepath.Join(dir, name), nil
}

func cmpDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

// sortTabs restores creation order, which the page map does not preserve.
func sortTabs(tabs []browser.Tab) {
	slices.SortFunc(tabs, func(a, b browser.Tab) int { return tabIndex(a) - tabIndex(b) })
}

func tabIndex(t browser.Tab) int {
	n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "tab-"))
	if err != nil {
		return 0
	}
	return n
}
