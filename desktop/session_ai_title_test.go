package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/sessioncatalog"
)

type desktopSessionTitleProvider struct {
	started chan struct{}
	chunks  chan provider.Chunk
	request provider.Request
	// budget is what was left of the model round trip's deadline when the
	// request reached the provider, and streamedAt when that happened.
	budget     time.Duration
	streamedAt time.Time
}

func (p *desktopSessionTitleProvider) Name() string { return "desktop-session-title" }

func (p *desktopSessionTitleProvider) Stream(ctx context.Context, request provider.Request) (<-chan provider.Chunk, error) {
	p.request = request
	p.streamedAt = time.Now()
	if deadline, ok := ctx.Deadline(); ok {
		p.budget = time.Until(deadline)
	}
	if p.started != nil {
		close(p.started)
	}
	return p.chunks, nil
}

func TestAIRenameCanonicalSessionUsesDurableHistoryInsteadOfEmptyLegacyFile(t *testing.T) {
	for _, identity := range []string{"topic", "session-id", "session-route"} {
		t.Run(identity, func(t *testing.T) {
			app, ctrl, runtime, prov, path := newCanonicalTitleFixture(t)
			appendSessionTestMessage(t, runtime, "host", provider.Message{ID: "host", Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "hidden host policy"})
			appendSessionTestMessage(t, runtime, "user", provider.Message{ID: "user", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "wrapped model input", RawContent: "帮我制作扫雷游戏"})
			// Long tool work pushes the authored turn outside the recent window.
			for i := range 110 {
				id := fmt.Sprintf("tool-%d", i)
				appendSessionTestMessage(t, runtime, id, provider.Message{ID: id, Role: provider.RoleTool, Content: "tool output"})
			}
			// Compaction changes model context without erasing the UI transcript.
			if _, err := runtime.Session().AppendBatch(t.Context(), "compact", []session.Event{{Kind: "model/context-replace", Payload: []byte(`{"messages":[{"role":"assistant","content":"compacted summary"}],"reason":"compaction"}`)}}); err != nil {
				t.Fatal(err)
			}
			key := "topic-canonical"
			if identity != "topic" {
				app.tabs["test"].TopicID = ""
				key = runtime.Ref().SessionID
				if identity == "session-route" {
					key = sessionRoute(key)
				}
			}
			title, err := app.AIRenameSession(key)
			if err != nil || title != "制作扫雷游戏" {
				t.Fatalf("AIRenameSession = %q, %v", title, err)
			}
			if got, err := ctrl.SessionService().Query().Stat(t.Context(), runtime.Ref()); err != nil || got.Title != title {
				t.Fatalf("canonical title = %+v, %v", got, err)
			}
			if got := prov.request.Messages; len(got) != 2 || got[1].Content != "帮我制作扫雷游戏" {
				t.Fatalf("title prompt = %+v", got)
			}
			if bytes, err := os.ReadFile(path); err != nil || len(bytes) != 0 {
				t.Fatalf("legacy file changed: length=%d, err=%v", len(bytes), err)
			}
			if meta, ok, err := agent.LoadBranchMeta(path); err != nil || (ok && meta.CustomTitle != "") {
				t.Fatalf("canonical rename wrote legacy title: %+v, %v", meta, err)
			}
			if identity == "topic" {
				if got := loadTopicTitle("", key); got == title || app.tabs["test"].TopicTitle != title {
					t.Fatalf("sidebar title = %q, runtime title = %q", got, app.tabs["test"].TopicTitle)
				}
			}
		})
	}
}

// The model round trip owns the AI-title budget: control bounds that call at
// sessionTitleTimeout, measured from the call. A second host-level deadline
// over the whole operation would spend part of it on durable preparation —
// the snapshot, the flush and the history projection TitleMessages builds —
// so a long conversation on a slow host hands the model a short budget and
// eventually loses an already generated title to a deadline that belongs to
// the provider, reported as the opaque operation_failed.
func TestAISessionTitleBudgetBelongsToTheModelRoundTrip(t *testing.T) {
	// control.sessionTitleTimeout. The host must hand over all of it.
	const modelRoundTripBudget = 30 * time.Second
	app, _, runtime, prov, _ := newCanonicalTitleFixture(t)
	appendSessionTestMessage(t, runtime, "user", provider.Message{
		ID: "user", Role: provider.RoleUser, Origin: provider.MessageOriginUser,
		Content: "wrapped model input", RawContent: "帮我制作扫雷游戏",
	})
	// Enough durable history that its first projection build is measurable.
	for i := range 60 {
		id := fmt.Sprintf("tool-%d", i)
		appendSessionTestMessage(t, runtime, id, provider.Message{ID: id, Role: provider.RoleTool, Content: "tool output"})
	}

	started := time.Now()
	title, err := app.AIRenameSession("topic-canonical")
	if err != nil || title != "制作扫雷游戏" {
		t.Fatalf("AIRenameSession = %q, %v", title, err)
	}

	if prov.streamedAt.IsZero() {
		t.Fatal("the model was never asked for a title")
	}
	if prov.budget < modelRoundTripBudget-time.Second {
		t.Fatalf("durable preparation of %v left the model only %v of its %v budget",
			prov.streamedAt.Sub(started), prov.budget, modelRoundTripBudget)
	}
}

func TestAIRenameDoesNotRenameSiblingCreatedDuringGeneration(t *testing.T) {
	app, _, runtime, prov, _ := newCanonicalTitleFixture(t)
	appendSessionTestMessage(t, runtime, "user", provider.Message{ID: "user", Role: provider.RoleUser, Content: "rename only A"})
	prov.started, prov.chunks = make(chan struct{}), make(chan provider.Chunk, 2)
	done := make(chan error, 1)
	go func() { _, err := app.AIRenameSession(sessionRoute(runtime.Ref().SessionID)); done <- err }()
	select {
	case <-prov.started:
	case err := <-done:
		t.Fatalf("early result: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not start")
	}
	app.mu.Lock()
	app.tabs["sibling"] = &WorkspaceTab{ID: "sibling", Scope: "global", TopicID: "topic-canonical", TopicTitle: "B unchanged", SessionID: "sibling"}
	app.mu.Unlock()
	prov.chunks <- provider.Chunk{Type: provider.ChunkText, Text: "A changed"}
	prov.chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(prov.chunks)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if app.tabs["sibling"].TopicTitle != "B unchanged" {
		t.Fatal("late AI rename changed sibling title")
	}
}

func TestAIRenameCanonicalSessionDoesNotRequireTargetTab(t *testing.T) {
	app, _, runtime, prov, path := newCanonicalTitleFixture(t)
	appendSessionTestMessage(t, runtime, "user", provider.Message{
		ID: "user", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "rename a cold sidebar session",
	})
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, runtime.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().EnsureSessionTopic(t.Context(), runtime.Ref().SessionID, "cold-topic", "Cold topic"); err != nil {
		t.Fatal(err)
	}

	// Keep another conversation active only as the provider host. The target
	// has no tab/controller binding and must be resolved from durable identity.
	generator := newDesktopSessionTitleController(filepath.Dir(path), path, prov)
	t.Cleanup(generator.Close)
	app.mu.Lock()
	app.tabs["test"].Ctrl = generator
	app.tabs["test"].TopicID = "active-other-topic"
	app.tabs["test"].SessionID = "active-other-session"
	app.mu.Unlock()

	title, err := app.AIRenameSession(sessionRoute(runtime.Ref().SessionID))
	if err != nil || title != "制作扫雷游戏" {
		t.Fatalf("AIRenameSession = %q, %v", title, err)
	}
	if app.tabs["test"].TopicID != "active-other-topic" || app.tabs["test"].SessionID != "active-other-session" {
		t.Fatal("AI rename navigated away from the active conversation")
	}
	info, err := app.desktopSessionService("").Query().Stat(t.Context(), runtime.Ref())
	if err != nil || info.Title != title {
		t.Fatalf("cold target title = %+v, %v", info, err)
	}
}

func TestAIRenameCanonicalEmptySessionReturnsProductError(t *testing.T) {
	app, _, _, _, _ := newCanonicalTitleFixture(t)
	if _, err := app.AIRenameSession("topic-canonical"); err == nil || !strings.Contains(err.Error(), "session_operation:no_messages:") {
		t.Fatalf("empty session error = %v", err)
	}
}

func TestAIRenameSessionDeduplicatesSameTarget(t *testing.T) {
	app, _, runtime, prov, _ := newCanonicalTitleFixture(t)
	appendSessionTestMessage(t, runtime, "user", provider.Message{
		ID: "user", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "deduplicate this rename",
	})
	prov.started = make(chan struct{})
	prov.chunks = make(chan provider.Chunk, 2)
	first := make(chan error, 1)
	go func() {
		_, err := app.AIRenameSession("topic-canonical")
		first <- err
	}()
	<-prov.started
	if _, err := app.AIRenameSession("topic-canonical"); err == nil || !strings.Contains(err.Error(), "session_operation:operation_busy:") {
		t.Fatalf("duplicate rename error = %v", err)
	}
	prov.chunks <- provider.Chunk{Type: provider.ChunkText, Text: "Deduplicated title"}
	prov.chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(prov.chunks)
	if err := <-first; err != nil {
		t.Fatalf("first rename: %v", err)
	}
}

func TestAISessionTitleOldFinallyCannotClearNewOperation(t *testing.T) {
	app := NewApp()
	_, oldCancel := context.WithCancelCause(context.Background())
	defer oldCancel(context.Canceled)
	_, newCancel := context.WithCancelCause(context.Background())
	defer newCancel(context.Canceled)
	const key = "path:/session"
	app.aiSessionTitleInFlight[key] = aiSessionTitleOperation{ID: "old", Cancel: oldCancel}
	app.cancelAISessionTitle(key)
	app.aiSessionTitleInFlight[key] = aiSessionTitleOperation{ID: "new", Cancel: newCancel}
	app.finishAISessionTitle(key, "old")
	if got := app.aiSessionTitleInFlight[key].ID; got != "new" {
		t.Fatalf("old finally cleared operation %q, want new", got)
	}
	app.finishAISessionTitle(key, "new")
	if _, ok := app.aiSessionTitleInFlight[key]; ok {
		t.Fatal("owning finally did not clear completed operation")
	}
}

func TestInvalidateAuxiliaryProviderOperationsCancelsAndFencesRequests(t *testing.T) {
	app := NewApp()
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(context.Canceled) })
	app.aiSessionTitleInFlight["path:/cold"] = aiSessionTitleOperation{ID: "old", Cancel: cancel}
	before := app.auxiliaryProviderGeneration.Load()

	app.invalidateAuxiliaryProviderOperations()

	if app.auxiliaryProviderGeneration.Load() != before+1 {
		t.Fatalf("auxiliary provider generation did not advance")
	}
	if len(app.aiSessionTitleInFlight) != 0 {
		t.Fatalf("invalidated operations remain in flight: %+v", app.aiSessionTitleInFlight)
	}
	var operationErr *SessionOperationError
	if !errors.As(context.Cause(ctx), &operationErr) || operationErr.Code != "provider_unavailable" {
		t.Fatalf("cancellation cause = %v, want provider_unavailable", context.Cause(ctx))
	}
}

func newCanonicalTitleFixture(t *testing.T) (*App, *control.Controller, *session.Runtime, *desktopSessionTitleProvider, string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	service := app.desktopSessionService(dir)
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "canonical-title", CWD: globalWorkspaceRoot()})
	if err != nil {
		t.Fatal(err)
	}
	path := agent.NewSessionPath(dir, "legacy-empty")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	chunks := make(chan provider.Chunk, 2)
	chunks <- provider.Chunk{Type: provider.ChunkText, Text: "制作扫雷游戏"}
	chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(chunks)
	prov := &desktopSessionTitleProvider{chunks: chunks}
	// The cold path uses real config/resolver assembly and a disposable HTTP
	// provider, not a controller borrowed from another conversation.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if prov.started != nil {
			close(prov.started)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			select {
			case <-r.Context().Done():
				return
			case chunk, ok := <-prov.chunks:
				if !ok || chunk.Type == provider.ChunkDone {
					fmt.Fprint(w, "data: [DONE]\n\n")
					return
				}
				body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": chunk.Text}}}})
				fmt.Fprintf(w, "data: %s\n\n", body)
				w.(http.Flusher).Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	cfg := config.LoadForEdit(config.UserConfigPath())
	cfg.DefaultModel = "test/title-model"
	cfg.Providers = []config.ProviderEntry{{Name: "test", Kind: "openai", Model: "title-model", BaseURL: server.URL, NoProxy: true}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspace, runtime.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().EnsureSessionTopic(t.Context(), runtime.Ref().SessionID, "topic-canonical", ""); err != nil {
		t.Fatal(err)
	}
	ctrl := control.New(control.Options{
		SessionDir: dir, SessionPath: path, ModelRef: "test/title-model",
		SessionService: service, SessionRuntime: runtime, ExclusiveSession: true,
		ProviderResolver: &provider.StaticResolver{Descriptors: []provider.Descriptor{{Ref: "test/title-model"}}, Providers: map[string]provider.Provider{"test/title-model": prov}},
	})
	t.Cleanup(ctrl.Close)
	installDesktopSessionTitleTab(app, ctrl, "topic-canonical", path)
	app.tabs["test"].SessionID = runtime.Ref().SessionID
	return app, ctrl, runtime, prov, path
}

func TestAIRenameCanonicalSessionPreservesManualRenameAndSurvivesTabClose(t *testing.T) {
	for _, change := range []string{"manual-title", "manual-topic", "binding"} {
		t.Run(change, func(t *testing.T) {
			app, ctrl, runtime, prov, _ := newCanonicalTitleFixture(t)
			appendSessionTestMessage(t, runtime, "user", provider.Message{ID: "user", Role: provider.RoleUser, Content: "rename this session"})
			prov.started = make(chan struct{})
			prov.chunks = make(chan provider.Chunk, 2)
			result := make(chan error, 1)
			go func() { _, err := app.AIRenameSession("topic-canonical"); result <- err }()
			select {
			case <-prov.started:
			case err := <-result:
				t.Fatalf("rename stopped before provider: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("provider did not start")
			}
			switch change {
			case "manual-title":
				if err := app.RenameSession(sessionRoute(runtime.Ref().SessionID), "manual title"); err != nil {
					t.Fatal(err)
				}
			case "manual-topic":
				if err := app.RenameTopic("topic-canonical", "manual topic title"); err != nil {
					t.Fatal(err)
				}
			default:
				app.mu.Lock()
				app.tabs["test"].Ctrl = nil
				app.mu.Unlock()
			}
			prov.chunks <- provider.Chunk{Type: provider.ChunkText, Text: "stale AI title"}
			prov.chunks <- provider.Chunk{Type: provider.ChunkDone}
			close(prov.chunks)
			resultErr := <-result
			if change == "binding" {
				if resultErr != nil {
					t.Fatalf("tab close cancelled persistent rename: %v", resultErr)
				}
			} else if resultErr == nil {
				t.Fatal("manual title change did not reject stale AI completion")
			}
			info, err := ctrl.SessionService().Query().Stat(t.Context(), runtime.Ref())
			if err != nil ||
				(change != "binding" && info.Title == "stale AI title") ||
				(change == "binding" && info.Title != "stale AI title") ||
				(change == "manual-title" && info.Title != "manual title") {
				t.Fatalf("title = %+v, %v", info, err)
			}
			if change == "manual-topic" && info.Title != "manual topic title" {
				t.Fatal("manual sidebar title was overwritten")
			}
		})
	}
}

func TestAIRenameSessionReadFailureIsNotEmptyHistory(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "unreadable")
	if err := os.WriteFile(path, []byte("invalid JSON\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctrl := newDesktopSessionTitleController(dir, path, &desktopSessionTitleProvider{})
	defer ctrl.Close()
	app := NewApp()
	installDesktopSessionTitleTab(app, ctrl, "topic-error", path)
	if _, err := app.AIRenameSession("topic-error"); err == nil || !strings.Contains(err.Error(), "session_operation:operation_failed:") || strings.Contains(err.Error(), dir) {
		t.Fatalf("read error = %v", err)
	}
}

func newDesktopSessionTitleController(dir, path string, prov provider.Provider) *control.Controller {
	return control.New(control.Options{
		SessionDir:  dir,
		SessionPath: path,
		ModelRef:    "test/title-model",
		ProviderResolver: &provider.StaticResolver{
			Descriptors: []provider.Descriptor{{Ref: "test/title-model"}},
			Providers:   map[string]provider.Provider{"test/title-model": prov},
		},
	})
}

func installDesktopSessionTitleTab(app *App, ctrl *control.Controller, topicID, path string) {
	app.setTestCtrl(ctrl, "test/title-model")
	app.mu.Lock()
	tab := app.tabs["test"]
	tab.TopicID = topicID
	tab.SessionPath = path
	app.mu.Unlock()
}

func TestAIRenameSessionWritesCanonicalAndLegacyTitles(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "title-test")
	writeHistoryTestSession(t, path, "debug the login redirect loop")
	chunks := make(chan provider.Chunk, 2)
	chunks <- provider.Chunk{Type: provider.ChunkText, Text: `"Debug login redirect loop"`}
	chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(chunks)
	ctrl := newDesktopSessionTitleController(dir, path, &desktopSessionTitleProvider{chunks: chunks})
	app := NewApp()
	installDesktopSessionTitleTab(app, ctrl, "topic-login", path)
	defer ctrl.Close()

	title, err := app.AIRenameSession("topic-login")
	if err != nil {
		t.Fatalf("AIRenameSession: %v", err)
	}
	if title != "Debug login redirect loop" {
		t.Fatalf("title = %q", title)
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok || meta.CustomTitle != title {
		t.Fatalf("meta = %+v, ok=%v, err=%v", meta, ok, err)
	}
	if got := loadSessionTitles(dir)[filepath.Base(path)]; got != title {
		t.Fatalf("legacy title = %q", got)
	}
}

func TestAIRenameSessionRejectsStaleProviderCompletion(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	first := agent.NewSessionPath(dir, "first")
	second := agent.NewSessionPath(dir, "second")
	writeHistoryTestSession(t, first, "first conversation")
	writeHistoryTestSession(t, second, "second conversation")
	started := make(chan struct{})
	chunks := make(chan provider.Chunk, 2)
	ctrl := newDesktopSessionTitleController(dir, first, &desktopSessionTitleProvider{started: started, chunks: chunks})
	app := NewApp()
	installDesktopSessionTitleTab(app, ctrl, "topic-race", first)
	defer ctrl.Close()

	result := make(chan error, 1)
	go func() {
		_, err := app.AIRenameSession("topic-race")
		result <- err
	}()
	<-started
	ctrl.SetSessionPath(second)
	app.mu.Lock()
	app.tabs["test"].SessionPath = second
	app.mu.Unlock()
	chunks <- provider.Chunk{Type: provider.ChunkText, Text: "Stale title"}
	chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(chunks)

	if err := <-result; err == nil || !strings.Contains(err.Error(), "session_operation:target_changed:") {
		t.Fatalf("stale completion error = %v", err)
	}
	for _, path := range []string{first, second} {
		meta, ok, err := agent.LoadBranchMeta(path)
		if err != nil {
			t.Fatal(err)
		}
		if ok && meta.CustomTitle != "" {
			t.Fatalf("stale completion renamed %s to %q", path, meta.CustomTitle)
		}
	}
}

func TestAIRenameSessionRejectsCompletionAfterManualRename(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "manual-wins")
	writeHistoryTestSession(t, path, "original conversation")
	started := make(chan struct{})
	chunks := make(chan provider.Chunk, 2)
	ctrl := newDesktopSessionTitleController(dir, path, &desktopSessionTitleProvider{started: started, chunks: chunks})
	app := NewApp()
	installDesktopSessionTitleTab(app, ctrl, "topic-manual", path)
	defer ctrl.Close()

	result := make(chan error, 1)
	go func() {
		_, err := app.AIRenameSession("topic-manual")
		result <- err
	}()
	<-started
	if err := app.RenameSession(path, "Newer manual title"); err != nil {
		t.Fatal(err)
	}
	chunks <- provider.Chunk{Type: provider.ChunkText, Text: "Stale AI title"}
	chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(chunks)

	if err := <-result; err == nil || !strings.Contains(err.Error(), "title changed") {
		t.Fatalf("stale AI completion error = %v", err)
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok || meta.CustomTitle != "Newer manual title" {
		t.Fatalf("manual title was overwritten: meta=%+v ok=%v err=%v", meta, ok, err)
	}
}

func TestDelayedTitleCallbackProjectsCurrentCanonicalTitle(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "projection-race")
	writeHistoryTestSession(t, path, "original conversation")
	app := NewApp()

	if err := agent.RenameSession(path, "AI title"); err != nil {
		t.Fatal(err)
	}
	if err := agent.RenameSession(path, "Newer manual title"); err != nil {
		t.Fatal(err)
	}
	if err := app.onSessionTitleChanged(dir, path, "Newer manual title"); err != nil {
		t.Fatal(err)
	}
	// Simulate the older AI callback resuming after the newer manual callback.
	if err := app.onSessionTitleChanged(dir, path, "AI title"); err != nil {
		t.Fatal(err)
	}

	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok || meta.CustomTitle != "Newer manual title" {
		t.Fatalf("canonical title = %+v, ok=%v, err=%v", meta, ok, err)
	}
	if got := loadSessionTitles(dir)[filepath.Base(path)]; got != "Newer manual title" {
		t.Fatalf("legacy projection = %q, want canonical newer title", got)
	}
}

func TestIndependentSessionTitleOverridesSharedTopicTitle(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	topicID := "metadata-title"
	path := writeTopicSessionWithPrompt(t, dir, "metadata-title.jsonl", topicID, "Original topic", "", "first prompt", time.Now())
	if err := ensureTopicIndexed("global", "", topicID, "Original topic", topicTitleSourceManual); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	installSessionCatalogForTest(t, app, dir, "global", "")
	catalog := app.sessionCatalog.Load()
	if err := app.syncSessionCatalogMetadata(context.Background(), catalog); err != nil {
		t.Fatal(err)
	}
	if err := agent.RenameSession(path, "AI session title"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.IndexSessionPath(context.Background(), sessioncatalog.DirectoryTarget{Path: dir, Scope: "global"}, path); err != nil {
		t.Fatal(err)
	}
	if err := app.syncSessionCatalogMetadata(context.Background(), catalog); err != nil {
		t.Fatal(err)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Label != "AI session title" {
		t.Fatalf("independent session title was not projected: %+v", page.Items)
	}
	if meta, ok, err := agent.LoadBranchMeta(path); err != nil || !ok || meta.CustomTitle != "AI session title" {
		t.Fatalf("independent session title was not preserved: meta=%+v ok=%v err=%v", meta, ok, err)
	}
}

func TestSessionTitleTranscriptAndPreviewAreBounded(t *testing.T) {
	transcript := sessionTitleTranscript([]string{
		strings.Repeat("a", aiSessionTitleMaxTurnRunes+10), "second", "third", "ignored",
	})
	parts := strings.Split(transcript, "\n\n")
	if len(parts) != aiSessionTitleMaxTurns || len([]rune(parts[0])) != aiSessionTitleMaxTurnRunes {
		t.Fatalf("parts = %d first runes = %d", len(parts), len([]rune(parts[0])))
	}
	records := []sessioncatalog.SessionRecord{{Path: "/sessions/a.jsonl", Preview: "full first-message preview"}}
	if got := topicSessionPreview(records, "/sessions/a.jsonl"); got != "full first-message preview" {
		t.Fatalf("preview = %q", got)
	}
}

func TestControllerForTopicPrefersActiveAndRejectsAmbiguousBackgroundTabs(t *testing.T) {
	app := NewApp()
	ctrlA := control.New(control.Options{})
	ctrlB := control.New(control.Options{})
	defer ctrlA.Close()
	defer ctrlB.Close()
	app.tabs = map[string]*WorkspaceTab{
		"a": {ID: "a", TopicID: "shared", Ctrl: ctrlA, Ready: true},
		"b": {ID: "b", TopicID: "shared", Ctrl: ctrlB, Ready: true},
	}
	app.activeTabID = "b"
	if got := app.controllerForTopic("shared"); got != ctrlB {
		t.Fatalf("active controller = %p, want %p", got, ctrlB)
	}
	app.activeTabID = "other"
	if got := app.controllerForTopic("shared"); got != nil {
		t.Fatalf("ambiguous background controller = %p, want nil", got)
	}
}
