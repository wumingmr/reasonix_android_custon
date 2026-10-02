package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"reasonix/internal/config"
)

func TestRegisterRemoteTabOpenReviveCarriesSelectedTitle(t *testing.T) {
	ref := RemoteTabRef{HostID: "box", Workspace: "~/app"}
	existing := &remoteTab{
		id: "remote-1", ref: ref, state: "disconnected", topicTitle: "Old title",
		session: remoteTabSessionState{name: "old", path: "/sessions/old.jsonl"},
		routing: remoteTabSessionRouting{currentPath: "/sessions/old.jsonl", running: map[string]bool{}},
	}
	a := &App{remoteTabs: map[string]*remoteTab{existing.id: existing}}
	registration := a.registerRemoteTabOpen(&remoteTab{id: "unused", ref: ref}, "Box", RemoteTabOpenOptions{
		SessionName: "selected", SessionPath: "/sessions/selected.jsonl", SessionTitle: "Selected title",
	})
	if registration.reuseID != existing.id || !registration.revive {
		t.Fatalf("registration = %+v, want revived %q", registration, existing.id)
	}
	if existing.topicTitle != "Old title" || existing.routing.currentPath != "/sessions/old.jsonl" {
		t.Fatalf("registration changed identity before visibility commit: title=%q path=%q", existing.topicTitle, existing.routing.currentPath)
	}
	if !a.commitRemoteTabOpenRegistration(&registration, "Box", RemoteTabOpenOptions{
		SessionName: "selected", SessionPath: "/sessions/selected.jsonl", SessionTitle: "Selected title",
	}) {
		t.Fatal("commitRemoteTabOpenRegistration rejected the reused shell")
	}
	if existing.topicTitle != "Selected title" {
		t.Fatalf("revived title = %q, want selected title", existing.topicTitle)
	}
}

// TestRemoteTabBridgeEntersNewSessionAndStreams pins the happy path: open →
// handshake → pump subscribed → POST /new → ready, with frames forwarded on
// the tab's event channel and the session cookie riding the jar.
func TestRemoteTabBridgeEntersNewSessionAndStreams(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	fs.newSessionPath = "/sessions/fresh.jsonl"
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	log := &eventLog{}
	a := &App{remoteRuntime: kernel, remoteEventHook: log.add}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")
	newCalled, _, cookieOnNew := fs.snapshot()
	if newCalled != 1 {
		t.Fatalf("POST /new called %d times, want 1", newCalled)
	}
	if !cookieOnNew {
		t.Fatal("POST /new carried no session cookie; handshake did not populate the jar")
	}
	if got := log.count("remote-tab:" + meta.ID + ":event"); got < 2 {
		t.Fatalf("pump forwarded %d frames, want ≥2 (events: %v)", got, log.events)
	}
	fs.mu.Lock()
	eventsQuery := fs.eventsQuery
	fs.mu.Unlock()
	if eventsQuery != "all=1" {
		t.Fatalf("event stream query = %q, want all=1", eventsQuery)
	}
	a.remoteTabMu.Lock()
	currentPath := a.remoteTabs[meta.ID].routing.currentPath
	a.remoteTabMu.Unlock()
	if currentPath != "/sessions/fresh.jsonl" {
		t.Fatalf("current session path = %q, want response header path", currentPath)
	}
	// State becomes observable immediately before its event is emitted. Wait for
	// the event too so a slow runner cannot sample that intentional handoff gap.
	waitForRemoteEventCount(t, log, "remote-tab:"+meta.ID+":state", 2)

	// Cancelling the pump (close/reconnect) must exit silently: no error
	// state is emitted for a deliberate stop.
	a.remoteTabMu.Lock()
	cancel := a.remoteTabs[meta.ID].cancel
	a.remoteTabMu.Unlock()
	if cancel != nil {
		cancel()
	}
	time.Sleep(100 * time.Millisecond)
	a.remoteTabMu.Lock()
	state := a.remoteTabs[meta.ID].state
	a.remoteTabMu.Unlock()
	if state != "ready" {
		t.Fatalf("state after pump cancel = %q, want ready (silent exit)", state)
	}
}

func TestConcurrentRemoteProjectOpenReusesOneTab(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	start := make(chan struct{})
	results := make(chan TabMeta, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true})
			results <- meta
			errs <- err
		}()
	}
	close(start)
	first, second := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("concurrent opens returned %q and %q", first.ID, second.ID)
	}
	a.remoteTabMu.Lock()
	count := len(a.remoteTabs)
	a.remoteTabMu.Unlock()
	if count != 1 {
		t.Fatalf("remote tab count = %d, want one", count)
	}
}

// TestRemoteTabBridgeToleratesTrailingSlashBase pins the production LocalURL
// shape: EnsureServer reports "http://127.0.0.1:port/" with a trailing slash.
// Naive base+"/auth/token" concatenation used to hit "//auth/token", which the
// serve auth gate rejects with 401 before routing the endpoint — the handshake
// died on every wizard-completed connect.
func TestRemoteTabBridgeToleratesTrailingSlashBase(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL + "/"},
		ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")
	newCalled, _, cookieOnNew := fs.snapshot()
	if newCalled != 1 || !cookieOnNew {
		t.Fatalf("handshake with trailing-slash base failed: /new=%d cookie=%v", newCalled, cookieOnNew)
	}
}

func TestRemoteTabBusyAttachKeepsCurrentSessionMetadata(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{
		{Name: "current", Path: "/sessions/current.jsonl", Title: "Current work", Current: true},
	})
	fs.mu.Lock()
	fs.failEnter = "cannot start a new session while a turn is running"
	fs.mu.Unlock()
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	reset, title := tab.session.reset, tab.topicTitle
	a.remoteTabMu.Unlock()
	if reset {
		t.Fatal("a refused /new must not mark the current session as a blank reset")
	}
	if title == a.localizedDefaultTopicTitle() {
		t.Fatalf("a refused /new replaced the current title with %q", title)
	}
	if err := a.SubmitRemoteTab(meta.ID, "continue"); err != nil {
		t.Fatalf("busy attach did not keep the current session usable: %v", err)
	}
}

// TestRemoteTabBridgeHandshakeFailureSurfacesError pins that a rejected
// token lands the tab in error instead of a phantom ready shell.
func TestRemoteTabBridgeHandshakeFailureSurfacesError(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "wrong-token",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)

	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "error")
	a.remoteTabMu.Lock()
	tabErr := a.remoteTabs[meta.ID].err
	a.remoteTabMu.Unlock()
	if !strings.Contains(tabErr, "401") {
		t.Fatalf("tab error = %q, want handshake 401", tabErr)
	}
	if _, _, cookieOnNew := fs.snapshot(); cookieOnNew {
		t.Fatal("POST /new must not be reached after a failed handshake")
	}
}

// TestRemoteTabBridgeResumeResolvesSessionPath pins the name→path
// resolution: a SessionName open reads GET /sessions and POSTs /resume with
// the entry's path, never the bare name.
func TestRemoteTabBridgeResumeResolvesSessionPath(t *testing.T) {
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{
		{Name: "s1", Path: "/remote/sessions/s1.jsonl", Title: "First", Turns: 2},
	})
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{SessionName: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")

	newCalled, resumePath, _ := fs.snapshot()
	if newCalled != 0 {
		t.Fatalf("POST /new called %d times, want 0 for a resume open", newCalled)
	}
	if resumePath != "/remote/sessions/s1.jsonl" {
		t.Fatalf("POST /resume path = %q, want the /sessions entry path", resumePath)
	}
}

// TestRemoteTabCommandRejectsUnknownOrUnreadyTab: an unknown tabID and a
// tab that has not finished bootstrap are errors, never silent no-ops.
func TestRemoteTabCommandRejectsUnknownOrUnreadyTab(t *testing.T) {
	a := &App{}
	if err := a.SubmitRemoteTab("missing", "hi"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("unknown tab err = %v, want not connected", err)
	}
	a.remoteTabMu.Lock()
	a.remoteTabs = map[string]*remoteTab{"booting": {id: "booting", state: "connecting"}}
	a.remoteTabMu.Unlock()
	if err := a.SubmitRemoteTab("booting", "hi"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("unready tab err = %v, want not connected", err)
	}
	a.remoteTabMu.Lock()
	a.remoteTabs["switching"] = &remoteTab{
		id: "switching", state: "ready", client: &http.Client{},
		routing: remoteTabSessionRouting{currentPath: "session-id:old", rehydratingPath: "session-id:new"},
	}
	a.remoteTabMu.Unlock()
	if err := a.SubmitRemoteTab("switching", "hi"); err == nil || !strings.Contains(err.Error(), "switching sessions") {
		t.Fatalf("session-switching tab err = %v, want switching-session guard", err)
	}
}

// TestModelsForTabRemoteUsesDesktopCatalog: a remote tab's model switcher
// lists the desktop provider catalog. Current is the desktop-owned tab model,
// not the remote serve GET /models payload.
func TestModelsForTabRemoteUsesDesktopCatalog(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	cfg := config.Default()
	cfg.DefaultModel = "deepseek/deepseek-v4-flash"
	cfg.Desktop.ProviderAccess = []string{"deepseek"}
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name: "deepseek", Kind: "anthropic", BaseURL: "https://api.deepseek.com/anthropic",
		Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"}, Default: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY",
	})
	if err := cfg.UpsertRemoteHost(config.RemoteHostEntry{Name: "box", Host: "127.0.0.1", Port: 22, User: "dev", CredentialMode: "local-proxy"}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	fs := newFakeServe(t, "s3cret", nil)
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})

	got := a.ModelsForTab(meta.ID)
	refs := map[string]bool{}
	current := ""
	for _, m := range got {
		refs[m.Ref] = true
		if m.Current {
			current = m.Ref
		}
	}
	if !refs["deepseek/deepseek-v4-flash"] || !refs["deepseek/deepseek-v4-pro"] {
		t.Fatalf("ModelsForTab(%s) = %+v, want desktop deepseek catalog", meta.ID, got)
	}
	if refs["remote/chat"] {
		t.Fatalf("ModelsForTab leaked the serve catalog: %+v", got)
	}
	if current != "deepseek/deepseek-v4-flash" {
		t.Fatalf("current = %q, want desktop default_model", current)
	}
}

// TestSetModelForTabRemoteOwnsModelOnDesktop: picking a model on a remote tab
// delegates the failure-atomic switch to the kernel before committing desktop
// tab metadata; the binding itself must not issue a second Serve request.
func TestSetModelForTabRemoteOwnsModelOnDesktop(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	cfg := config.Default()
	cfg.DefaultModel = "deepseek/deepseek-v4-flash"
	cfg.Desktop.ProviderAccess = []string{"deepseek"}
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name: "deepseek", Kind: "anthropic", BaseURL: "https://api.deepseek.com/anthropic",
		Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"}, Default: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY",
	})
	if err := cfg.UpsertRemoteHost(config.RemoteHostEntry{Name: "box", Host: "127.0.0.1", Port: 22, User: "dev", CredentialMode: "local-proxy"}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	fs := newFakeServe(t, "s3cret", nil)
	fs.newSessionPath = "/remote/sessions/model-switch.jsonl"
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})

	if err := a.SetModelForTab(meta.ID, "deepseek/deepseek-v4-pro"); err != nil {
		t.Fatalf("SetModelForTab: %v", err)
	}
	if len(kernel.switchProxyCalls) != 1 || kernel.switchProxyCalls[0] != [5]string{"box", "~/app", "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-pro", fs.newSessionPath} {
		t.Fatalf("credential proxy switch calls = %+v", kernel.switchProxyCalls)
	}
	for _, c := range fs.recorded() {
		if strings.HasPrefix(c, "POST /model") {
			t.Fatalf("serve saw %v, binding duplicated the kernel-owned switch", fs.recorded())
		}
	}
	var current string
	for _, m := range a.ModelsForTab(meta.ID) {
		if m.Current {
			current = m.Ref
		}
	}
	if current != "deepseek/deepseek-v4-pro" {
		t.Fatalf("current = %q, want deepseek/deepseek-v4-pro", current)
	}
}
