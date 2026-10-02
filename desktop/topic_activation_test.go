package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/evidence"
)

// activationEventRecorder captures "topic:activation" events through the
// synchronous test hook, in emission order.
type activationEventRecorder struct {
	ch chan TopicActivationEvent
}

func newActivationEventRecorder(app *App) *activationEventRecorder {
	r := &activationEventRecorder{ch: make(chan TopicActivationEvent, 64)}
	app.activationEventHook = func(ev TopicActivationEvent) { r.ch <- ev }
	return r
}

// next returns the next event, failing the test if none arrives.
func (r *activationEventRecorder) next(t *testing.T) TopicActivationEvent {
	t.Helper()
	select {
	case ev := <-r.ch:
		return ev
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for a topic activation event")
		return TopicActivationEvent{}
	}
}

// waitFor drains events until pred matches and returns that event.
func (r *activationEventRecorder) waitFor(t *testing.T, pred func(TopicActivationEvent) bool) TopicActivationEvent {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-r.ch:
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for the expected topic activation event")
			return TopicActivationEvent{}
		}
	}
}

// drainEmpty asserts no further events are pending.
func (r *activationEventRecorder) drainEmpty(t *testing.T) {
	t.Helper()
	for {
		select {
		case ev := <-r.ch:
			t.Fatalf("unexpected extra topic activation event: %+v", ev)
		default:
			return
		}
	}
}

// tabBuildGate blocks every tab controller build at its entry until the test
// releases that tab, letting activation tests force out-of-order build
// completion without sleeps.
type tabBuildGate struct {
	mu      sync.Mutex
	gates   map[string]chan struct{}
	entered chan string
}

func newTabBuildGate(app *App) *tabBuildGate {
	g := &tabBuildGate{
		gates:   map[string]chan struct{}{},
		entered: make(chan string, 64),
	}
	app.tabBuildStartHook = func(tabID string) {
		g.mu.Lock()
		ch := g.gates[tabID]
		if ch == nil {
			ch = make(chan struct{})
			g.gates[tabID] = ch
		}
		g.mu.Unlock()
		g.entered <- tabID
		<-ch
	}
	return g
}

// waitEntered fails the test unless a build for tabID reaches the gate.
func (g *tabBuildGate) waitEntered(t *testing.T, tabID string) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case got := <-g.entered:
			if got == tabID {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for tab %q build to reach the gate", tabID)
		}
	}
}

func (g *tabBuildGate) release(tabID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch := g.gates[tabID]; ch != nil {
		close(ch)
		delete(g.gates, tabID)
	}
}

// releaseAll unblocks every gated build; safe to call from test cleanup.
func (g *tabBuildGate) releaseAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, ch := range g.gates {
		close(ch)
		delete(g.gates, id)
	}
}

func activationEventFor(requestID, phase string) func(TopicActivationEvent) bool {
	return func(ev TopicActivationEvent) bool {
		return ev.RequestID == requestID && ev.Phase == phase
	}
}

// flushActivationCompletions gives superseded completion goroutines a
// deterministic sync point: they must pass singleSurfaceMu before doing
// anything observable, so round-tripping the mutex behind them (blocked
// Lockers queue FIFO) proves their guarded-off generation check has run.
func flushActivationCompletions(app *App) {
	done := make(chan struct{})
	go func() {
		app.singleSurfaceMu.Lock()
		defer app.singleSurfaceMu.Unlock()
		close(done)
	}()
	<-done
}

func TestStartTopicActivationSyncBuildAndReuseFastPath(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp() // a.ctx == nil: builds run synchronously inside the call
	app.readyHook = func() {}
	installNoopRuntimeEvents(app)
	events := newActivationEventRecorder(app)
	t.Cleanup(func() { app.shutdown(context.Background()) })

	ticket, err := app.StartTopicActivation(TopicActivationRequest{Scope: "global", TopicID: "topic-sync", RequestID: "req-1"})
	if err != nil {
		t.Fatalf("StartTopicActivation: %v", err)
	}
	if got := events.next(t); got.Phase != "starting" || got.RequestID != "req-1" {
		t.Fatalf("event = %+v, want starting req-1", got)
	}
	ready := events.waitFor(t, activationEventFor("req-1", "ready"))
	if ready.TabID != ticket.TabID {
		t.Fatalf("ready tab = %q, want %q", ready.TabID, ticket.TabID)
	}

	app.mu.RLock()
	tab := app.tabs[ticket.TabID]
	var ctrlAfterFirst control.SessionAPI
	if tab != nil {
		ctrlAfterFirst = tab.Ctrl
	}
	app.mu.RUnlock()
	if tab == nil || ctrlAfterFirst == nil {
		t.Fatal("tab missing or controller not built after ready")
	}

	// Reuse/fast path: reactivating the same topic reuses the tab, starts no
	// new build, and still emits exactly one ready after pruning.
	ticket2, err := app.StartTopicActivation(TopicActivationRequest{Scope: "global", TopicID: "topic-sync", RequestID: "req-2"})
	if err != nil {
		t.Fatalf("StartTopicActivation reuse: %v", err)
	}
	if ticket2.TabID != ticket.TabID {
		t.Fatalf("reuse ticket tab = %q, want reused %q", ticket2.TabID, ticket.TabID)
	}
	if got := events.next(t); got.Phase != "starting" || got.RequestID != "req-2" {
		t.Fatalf("event = %+v, want starting req-2", got)
	}
	events.waitFor(t, activationEventFor("req-2", "ready"))
	flushActivationCompletions(app)
	events.drainEmpty(t)

	app.mu.RLock()
	sameTab := app.tabs[ticket.TabID] == tab
	sameCtrl := tab.Ctrl == ctrlAfterFirst
	tabCount := len(app.tabs)
	app.mu.RUnlock()
	if !sameTab || !sameCtrl {
		t.Fatal("reuse activation rebuilt or replaced the tab/controller")
	}
	if tabCount != 1 {
		t.Fatalf("tab count = %d, want 1", tabCount)
	}
}

func TestStartTopicActivationReplacesRemoteSurface(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	installNoopRuntimeEvents(app)
	events := newActivationEventRecorder(app)
	gate := newTabBuildGate(app)
	t.Cleanup(func() { gate.releaseAll(); app.shutdown(context.Background()) })

	remoteCtx, cancelRemote := context.WithCancel(context.Background())
	app.remoteTabMu.Lock()
	app.remoteTabs = map[string]*remoteTab{
		"remote": {id: "remote", ref: RemoteTabRef{HostID: "box", Workspace: "/work"}, state: "disconnected", cancel: cancelRemote},
	}
	app.remoteTabLayout = remoteTabLayoutState{activeID: "remote", order: []string{"remote"}, stripOrder: []string{"remote"}}
	app.remoteTabMu.Unlock()

	ticket, err := app.StartTopicActivation(TopicActivationRequest{Scope: "global", TopicID: "local-after-remote", RequestID: "local-after-remote"})
	if err != nil {
		t.Fatal(err)
	}
	// The tab build stays held: the local tab must be selected without it.
	gate.waitEntered(t, ticket.TabID)
	if tabs := app.ListTabs(); len(tabs) == 0 {
		t.Fatalf("tabs immediately after local ticket = %+v, want active %q", tabs, ticket.TabID)
	} else {
		active := ""
		for _, tab := range tabs {
			if tab.Active {
				active = tab.ID
			}
		}
		if active != ticket.TabID {
			t.Fatalf("tabs immediately after local ticket = %+v, want active %q", tabs, ticket.TabID)
		}
	}
	gate.release(ticket.TabID)
	events.waitFor(t, activationEventFor(ticket.RequestID, "ready"))
	flushActivationCompletions(app)
	if tabs := app.ListTabs(); len(tabs) != 1 || tabs[0].ID != ticket.TabID || !tabs[0].Active {
		t.Fatalf("tabs after local activation = %+v, want only active local %q", tabs, ticket.TabID)
	}
	select {
	case <-remoteCtx.Done():
	default:
		t.Fatal("remote tab pump was not cancelled after switching to local")
	}
}

// activationStubController is the minimal SessionAPI surface exercised by
// prune/detach/attach flows around a tab with active runtime work.
type activationStubController struct {
	stubSessionAPI
	sessionPath string
	closed      atomic.Bool
	status      *control.RuntimeStatus
}

func (c *activationStubController) RuntimeStatus() control.RuntimeStatus {
	if c.status != nil {
		return *c.status
	}
	return control.RuntimeStatus{Running: true}
}
func (c *activationStubController) SessionPath() string      { return c.sessionPath }
func (c *activationStubController) SetSessionPath(p string)  { c.sessionPath = p }
func (c *activationStubController) SessionDir() string       { return "" }
func (c *activationStubController) Snapshot() error          { return nil }
func (c *activationStubController) Cancel()                  {}
func (c *activationStubController) Close()                   { c.closed.Store(true) }
func (c *activationStubController) Label() string            { return "stub-model" }
func (c *activationStubController) ReplayPendingPrompts()    {}
func (c *activationStubController) PlanMode() bool           { return false }
func (c *activationStubController) AutoApproveTools() bool   { return false }
func (c *activationStubController) ToolApprovalMode() string { return "" }
func (c *activationStubController) Goal() string             { return "" }
func (c *activationStubController) GoalStatus() string       { return "" }
func (c *activationStubController) Turn() int                { return 0 }
func (c *activationStubController) GoalRuntime() control.GoalRuntimeView {
	return control.GoalRuntimeView{}
}
func (c *activationStubController) Todos() []evidence.TodoItem { return nil }
func (c *activationStubController) SnapshotForShutdown() error { return nil }

func TestActivateTopicSupersedesPendingTicketedActivation(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()
	readyCh := make(chan struct{}, 8)
	app.readyHook = func() { readyCh <- struct{}{} }
	installNoopRuntimeEvents(app)
	events := newActivationEventRecorder(app)
	gate := newTabBuildGate(app)
	t.Cleanup(func() {
		gate.releaseAll()
		app.shutdown(context.Background())
	})

	ticketA, err := app.StartTopicActivation(TopicActivationRequest{Scope: "global", TopicID: "topic-a", RequestID: "req-a"})
	if err != nil {
		t.Fatalf("StartTopicActivation A: %v", err)
	}
	if got := events.next(t); got.Phase != "starting" {
		t.Fatalf("event = %+v, want starting", got)
	}
	gate.waitEntered(t, ticketA.TabID)
	app.mu.RLock()
	tabA := app.tabs[ticketA.TabID]
	var tabABuildDone chan struct{}
	if tabA != nil {
		// Snapshot the build-done channel now: closeTabBuildDone nils the
		// field after closing it, so reading it after the fact would block on
		// a nil channel forever.
		tabABuildDone = tabA.buildDone
	}
	app.mu.RUnlock()
	if tabA == nil {
		t.Fatal("tab A missing after activation start")
	}

	// A legacy ActivateTopic call supersedes the pending ticketed activation
	// and keeps its own synchronous contract: it returns after the prune.
	type legacyResult struct {
		meta TabMeta
		err  error
	}
	legacyDone := make(chan legacyResult, 1)
	go func() {
		meta, err := app.ActivateTopic("global", "", "topic-b", "")
		legacyDone <- legacyResult{meta: meta, err: err}
	}()
	if got := events.next(t); got != (TopicActivationEvent{RequestID: "req-a", TabID: ticketA.TabID, Phase: "cancelled"}) {
		t.Fatalf("event = %+v, want cancelled req-a", got)
	}
	// The legacy prune queues the runtime-admission WRITE lock. Go's RWMutex
	// blocks new readers behind a queued writer, so tab B's gated build may
	// not be able to enter the gate until the prune has run — release A first
	// (its build abandons via the superseded path), which unblocks the prune
	// in the interleaving where B's build is still stuck behind the writer.
	app.mu.RLock()
	var tabBID string
	for id := range app.tabs {
		if id != ticketA.TabID {
			tabBID = id
		}
	}
	app.mu.RUnlock()
	if tabBID == "" {
		t.Fatal("legacy activation did not open tab B")
	}
	gate.release(ticketA.TabID)
	gate.waitEntered(t, tabBID)
	gate.release(tabBID)

	var legacy legacyResult
	select {
	case legacy = <-legacyDone:
	case <-time.After(15 * time.Second):
		t.Fatal("legacy ActivateTopic did not return")
	}
	if legacy.err != nil {
		t.Fatalf("legacy ActivateTopic: %v", legacy.err)
	}
	if legacy.meta.ID != tabBID {
		t.Fatalf("legacy meta tab = %q, want %q", legacy.meta.ID, tabBID)
	}
	<-readyCh // B's build published and emitted agent:ready

	assertTabIDs(t, app.ListTabs(), tabBID)
	<-tabABuildDone // A's abandoned build terminated
	flushActivationCompletions(app)
	// The legacy path emits no activation events of its own, and the
	// superseded completion stays silent.
	events.drainEmpty(t)
}

func TestSetActiveTabSupersedesPendingPublication(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()
	readyCh := make(chan struct{}, 8)
	app.readyHook = func() { readyCh <- struct{}{} }
	installNoopRuntimeEvents(app)
	events := newActivationEventRecorder(app)
	t.Cleanup(func() { app.shutdown(context.Background()) })

	// Establish a settled visible tab through the legacy path (ungated).
	metaB, err := app.ActivateTopic("global", "", "topic-b", "")
	if err != nil {
		t.Fatalf("ActivateTopic B: %v", err)
	}
	select {
	case <-readyCh:
	case <-time.After(15 * time.Second):
		t.Fatal("tab B build did not finish")
	}

	gate := newTabBuildGate(app)
	t.Cleanup(gate.releaseAll)
	ticketA, err := app.StartTopicActivation(TopicActivationRequest{Scope: "global", TopicID: "topic-a", RequestID: "req-a"})
	if err != nil {
		t.Fatalf("StartTopicActivation A: %v", err)
	}
	if got := events.next(t); got.Phase != "starting" {
		t.Fatalf("event = %+v, want starting", got)
	}
	gate.waitEntered(t, ticketA.TabID)
	app.mu.RLock()
	tabA := app.tabs[ticketA.TabID]
	var tabABuildDone chan struct{}
	if tabA != nil {
		// Snapshot before the build terminates: closeTabBuildDone nils the
		// field after closing the channel.
		tabABuildDone = tabA.buildDone
	}
	app.mu.RUnlock()
	if tabA == nil || tabABuildDone == nil {
		t.Fatal("tab A missing or has no in-flight build")
	}

	// The user clicks tab B directly: the pending activation's publication
	// (prune + ready) is superseded, but its build is not cancelled — tab A
	// stays open and may legitimately become ready.
	if err := app.SetActiveTab(metaB.ID); err != nil {
		t.Fatalf("SetActiveTab B: %v", err)
	}
	if got := events.next(t); got != (TopicActivationEvent{RequestID: "req-a", TabID: ticketA.TabID, Phase: "cancelled"}) {
		t.Fatalf("event = %+v, want cancelled req-a", got)
	}
	gate.release(ticketA.TabID)
	select {
	case <-readyCh: // A's build still completes and publishes
	case <-time.After(15 * time.Second):
		t.Fatal("tab A build did not finish after SetActiveTab")
	}

	app.mu.RLock()
	tabAReady := tabA.Ready
	tabCount := len(app.tabs)
	active := app.activeTabID
	app.mu.RUnlock()
	if tabCount != 2 {
		t.Fatalf("tab count = %d, want 2 (SetActiveTab must not prune)", tabCount)
	}
	if active != metaB.ID {
		t.Fatalf("active tab = %q, want %q", active, metaB.ID)
	}
	if !tabAReady {
		t.Fatal("tab A build was cancelled or unpublished by SetActiveTab")
	}
	<-tabABuildDone
	flushActivationCompletions(app)
	events.drainEmpty(t)
}

func TestMetaForTabFastPathCachesExpensiveFields(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "CUSTOM_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "custom/vision-pro"
	cfg.Agent.VisionModel = "auto"
	cfg.Desktop.ProviderAccess = []string{"custom"}
	cfg.Providers = []config.ProviderEntry{{
		Name:         "custom",
		Kind:         "openai",
		BaseURL:      "https://example.invalid/v1",
		APIKeyEnv:    "CUSTOM_KEY",
		Models:       []string{"text-only", "vision-pro"},
		VisionModels: []string{"vision-pro"},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	repoRoot := t.TempDir()
	runGitIn(t, repoRoot, "init")
	runGitIn(t, repoRoot, "checkout", "-b", "feature/meta-cache")
	plainRoot := t.TempDir()

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	metaEvents := make(chan TabMetaRefreshEvent, 8)
	// NOTE: install the capture AFTER any installNoopRuntimeEvents call — that
	// helper overwrites app.runtimeEvents.emit.
	installNoopRuntimeEvents(app)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...any) {
		if name != tabMetaRefreshEventChannel || len(payload) == 0 {
			return
		}
		if ev, ok := payload[0].(TabMetaRefreshEvent); ok {
			metaEvents <- ev
		}
	}
	var loads atomic.Int32
	var blockLoad atomic.Bool
	blockLoad.Store(true)
	loadEntered := make(chan struct{})
	var loadOnce sync.Once
	releaseLoad := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			blockLoad.Store(false)
			close(releaseLoad)
		})
	}
	app.configLoadForRootHook = func(string) {
		loads.Add(1)
		loadOnce.Do(func() { close(loadEntered) })
		if blockLoad.Load() {
			<-releaseLoad
		}
	}

	tab := &WorkspaceTab{
		ID:            "meta-tab",
		Scope:         "project",
		WorkspaceRoot: repoRoot,
		Label:         "custom/vision-pro",
		model:         "custom/vision-pro",
		disabledMCP:   map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	installNoopRuntimeEvents(nil, tab.sink) // sink only — keep the capture on app.runtimeEvents
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		release()
		app.shutdown(context.Background())
	})

	// The request path never loads config: the first MetaForTab returns empty
	// expensive fields even while the background refresh is parked inside the
	// config-load hook.
	first := app.MetaForTab(tab.ID)
	if first.GitBranch != "" || first.ImageInputEnabled {
		t.Fatalf("first MetaForTab = branch %q image %v, want empty cached values", first.GitBranch, first.ImageInputEnabled)
	}
	select {
	case <-loadEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("background meta refresh never reached the config load")
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("config loads = %d, want exactly 1 (deduped background refresh)", got)
	}
	release()

	var refreshed TabMetaRefreshEvent
	select {
	case refreshed = <-metaEvents:
	case <-time.After(15 * time.Second):
		t.Fatal("no tab:meta event after the background refresh")
	}
	if refreshed.TabID != tab.ID {
		t.Fatalf("tab:meta tab = %q, want %q", refreshed.TabID, tab.ID)
	}
	if refreshed.Meta.GitBranch != "feature/meta-cache" {
		t.Fatalf("refreshed branch = %q, want feature/meta-cache", refreshed.Meta.GitBranch)
	}
	if !refreshed.Meta.ImageInputEnabled {
		t.Fatal("refreshed meta should enable image input for custom/vision-pro")
	}
	if !refreshed.Meta.VisionFallbackEnabled {
		t.Fatal("refreshed meta should expose the configured image-understanding fallback")
	}

	// A fresh cache serves subsequent calls without another config load.
	second := app.MetaForTab(tab.ID)
	if second.GitBranch != "feature/meta-cache" || !second.ImageInputEnabled {
		t.Fatalf("cached MetaForTab = branch %q image %v", second.GitBranch, second.ImageInputEnabled)
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("config loads after cache hit = %d, want 1", got)
	}

	// A root change invalidates conservatively: the other root's cached values
	// must not be served, and the background refresh repopulates for the new
	// root (not a git repo here, so the branch stays empty).
	app.mu.Lock()
	tab.WorkspaceRoot = plainRoot
	app.mu.Unlock()
	third := app.MetaForTab(tab.ID)
	if third.GitBranch != "" || third.ImageInputEnabled {
		t.Fatalf("MetaForTab after root change = branch %q image %v, want empty", third.GitBranch, third.ImageInputEnabled)
	}
	select {
	case ev := <-metaEvents:
		if ev.Meta.GitBranch != "" {
			t.Fatalf("refreshed branch for non-repo root = %q, want empty", ev.Meta.GitBranch)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no tab:meta event after the root change")
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("config loads after root change = %d, want 2", got)
	}
}
