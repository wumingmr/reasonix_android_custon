package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"reasonix/desktop/internal/draftstate"
	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

func newLegacyCleanupTestApp(t *testing.T) (*App, string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	app.legacyCleanup = legacycleanup.New(filepath.Join(t.TempDir(), "legacy-empty-session-cleanup-v1.json"))
	t.Cleanup(app.closeSessionServices)
	return app, t.TempDir()
}

func createLegacyCleanupSession(t *testing.T, app *App, workspaceRoot, id string, withUserMessage bool) (session.SessionRef, string) {
	t.Helper()
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	service := app.desktopSessionService("")
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: id, CWD: workspaceRoot, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetTitle(t.Context(), runtime.Ref(), defaultTopicTitle); err != nil {
		t.Fatal(err)
	}
	if withUserMessage {
		payload, _ := json.Marshal(map[string]any{"message": map[string]any{"id": "user-empty", "role": "user", "content": ""}})
		if _, err := runtime.Session().AppendBatch(t.Context(), "used", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, id, ""); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	return runtime.Ref(), workspaceID
}

func cleanupCandidate(t *testing.T, app *App, id string) legacycleanup.Candidate {
	t.Helper()
	state, err := app.legacyCleanup.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	item, ok := state.Items[id]
	if !ok {
		t.Fatalf("candidate %q missing from %+v", id, state.Items)
	}
	return item
}

func TestLegacyCleanupArchivesOnlyConclusiveEmptyDefaultSessionsWithoutFallback(t *testing.T) {
	app, workspaceRoot := newLegacyCleanupTestApp(t)
	empty, _ := createLegacyCleanupSession(t, app, workspaceRoot, "legacy-empty", false)
	used, _ := createLegacyCleanupSession(t, app, workspaceRoot, "legacy-used", true)
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	app.processLegacyCleanupSession(cleanupCandidate(t, app, "session:"+empty.SessionID))
	app.processLegacyCleanupSession(cleanupCandidate(t, app, "session:"+used.SessionID))
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[empty.SessionID].Lifecycle != workspacestate.Archived {
		t.Fatalf("empty lifecycle = %q", state.SessionStates[empty.SessionID].Lifecycle)
	}
	if state.SessionStates[used.SessionID].Lifecycle != workspacestate.Active {
		t.Fatalf("used lifecycle = %q", state.SessionStates[used.SessionID].Lifecycle)
	}
	if got := cleanupCandidate(t, app, "session:"+used.SessionID); got.Phase != "has_content" {
		t.Fatalf("used candidate = %+v", got)
	}
	app.mu.RLock()
	defer app.mu.RUnlock()
	if len(app.tabs) != 0 {
		t.Fatalf("cleanup opened fallback tabs: %+v", app.tabs)
	}
}

func TestLegacyCleanupCanonicalRestoreMarksCandidateProtected(t *testing.T) {
	app, workspaceRoot := newLegacyCleanupTestApp(t)
	ref, _ := createLegacyCleanupSession(t, app, workspaceRoot, "legacy-restore", false)
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	app.processLegacyCleanupSession(cleanupCandidate(t, app, "session:"+ref.SessionID))
	page, err := app.ListTrashEntries("", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	request := SessionLifecycleRequest{
		OperationID: "restore-cleaned-session", Action: "restore", ExpectedGeneration: page.Generation,
		Targets: []SessionLifecycleTarget{{Ref: &ref}},
	}
	result, err := app.ApplySessionLifecycle(request)
	if err != nil || !result.Committed {
		t.Fatalf("restore cleaned session = %+v, %v", result, err)
	}
	if got := cleanupCandidate(t, app, "session:"+ref.SessionID); !got.Restored || got.Phase != "restored" || got.Reason != "restored_by_user" {
		t.Fatalf("restored candidate = %+v", got)
	}
}

func TestLegacyCleanupFinalFenceRejectsSameTitleRewrite(t *testing.T) {
	app, workspaceRoot := newLegacyCleanupTestApp(t)
	ref, _ := createLegacyCleanupSession(t, app, workspaceRoot, "legacy-title-race", false)
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	app.legacyCleanupWorker.beforeArchive = func() {
		if err := app.desktopSessionService("").SetTitle(t.Context(), ref, defaultTopicTitle); err != nil {
			t.Fatal(err)
		}
	}
	app.processLegacyCleanupSession(cleanupCandidate(t, app, "session:"+ref.SessionID))
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[ref.SessionID].Lifecycle == workspacestate.Archived {
		t.Fatal("same-value title rewrite was archived using a stale decision")
	}
	if got := cleanupCandidate(t, app, "session:"+ref.SessionID); got.Phase != "protected" {
		t.Fatalf("race candidate = %+v", got)
	}
}

func TestLegacyCleanupProtectsDraftReservedSession(t *testing.T) {
	app, workspaceRoot := newLegacyCleanupTestApp(t)
	ref, workspaceID := createLegacyCleanupSession(t, app, workspaceRoot, "legacy-draft-reserved", false)
	draft, _, err := app.draftStore().Open(t.Context(), workspaceID, "project", workspaceRoot, "draft-protect", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.draftStore().BeginOperation(t.Context(), draftstate.Operation{
		ID: "draft-operation", DraftID: draft.ID, WorkspaceID: workspaceID, DraftRevision: draft.Revision,
		SessionID: ref.SessionID, SubmissionID: "submission", Fingerprint: "fingerprint", RequestJSON: `{}`, Phase: "reserved",
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	app.processLegacyCleanupSession(cleanupCandidate(t, app, "session:"+ref.SessionID))
	if got := cleanupCandidate(t, app, "session:"+ref.SessionID); got.Phase != "protected" || got.Reason != "draft_operation" {
		t.Fatalf("draft-associated candidate = %+v", got)
	}
}

func TestLegacyCleanupTreatsRestoredTabWithoutRuntimeAsBusy(t *testing.T) {
	app, workspaceRoot := newLegacyCleanupTestApp(t)
	ref, _ := createLegacyCleanupSession(t, app, workspaceRoot, "legacy-restored-tab", false)
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.tabs["restored-tab"] = &WorkspaceTab{ID: "restored-tab", SessionID: ref.SessionID}
	app.mu.Unlock()
	app.processLegacyCleanupSession(cleanupCandidate(t, app, "session:"+ref.SessionID))
	if got := cleanupCandidate(t, app, "session:"+ref.SessionID); got.Phase != "busy" || got.Reason != "session_open" {
		t.Fatalf("candidate = %+v", got)
	}
	app.mu.Lock()
	delete(app.tabs, "restored-tab")
	app.tabsRestored = make(chan struct{})
	close(app.tabsRestored)
	app.mu.Unlock()
	close(app.desktopMigrationDone)
	// Releasing a view no longer authorizes automatic historical cleanup.
	// Only the explicit maintenance operation may archive this candidate.
	app.runLegacyEmptySessionCleanup(false)
	if got := cleanupCandidate(t, app, "session:"+ref.SessionID); got.Phase != "archived" {
		t.Fatalf("candidate after runtime release = %+v", got)
	}
}

func TestLegacyCleanupTopicPlaceholderCanBeRestoredOnce(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	if _, err := app.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil {
		t.Fatal(err)
	}
	topic, err := app.CreateTopic("global", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "topic:"+topic.ID)
	app.processLegacyCleanupTopic(item)
	page, err := app.ListTrashEntries("", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var entry *TrashEntry
	for index := range page.Items {
		if page.Items[index].ID == item.ID {
			entry = &page.Items[index]
			break
		}
	}
	if entry == nil || entry.Ref != nil || entry.RecoveryEntryID == "" {
		t.Fatalf("topic placeholder trash entry = %+v", entry)
	}
	request := SessionLifecycleRequest{OperationID: "restore-placeholder", Action: "restore", ExpectedGeneration: page.Generation,
		Targets: []SessionLifecycleTarget{{WorkspaceID: item.WorkspaceID, RecoveryEntryID: entry.RecoveryEntryID}}}
	result, err := app.ApplySessionLifecycle(request)
	if err != nil || !result.Committed || !topicIndexedInRegistry("global", "", topic.ID) {
		t.Fatalf("restore placeholder = %+v, %v", result, err)
	}
	if got := cleanupCandidate(t, app, item.ID); !got.Restored || got.Phase != "restored" {
		t.Fatalf("restored candidate = %+v", got)
	}
	if err := app.restoreLegacyCleanupTopic(item.ID, item.WorkspaceID); err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
}

func TestLegacyCleanupTopicArchivePendingReconcilesAfterLostResult(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	if _, err := app.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil {
		t.Fatal(err)
	}
	topic, err := app.CreateTopic("global", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "topic:"+topic.ID)
	if !app.markLegacyCleanupTopicArchivePending(item.ID) {
		t.Fatal("failed to persist archive marker")
	}
	if err := app.deleteTopic(topic.ID); err != nil {
		t.Fatal(err)
	}
	item = cleanupCandidate(t, app, item.ID)
	if !app.reconcileLegacyCleanupTopicArchive(item) {
		t.Fatal("pending topic archive was not reconciled")
	}
	if got := cleanupCandidate(t, app, item.ID); got.Phase != "archived" {
		t.Fatalf("candidate = %+v", got)
	}
}

func TestLegacyCleanupTopicPlaceholderCanBePurgedWithoutSession(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	if _, err := app.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil {
		t.Fatal(err)
	}
	topic, err := app.CreateTopic("global", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "topic:"+topic.ID)
	app.processLegacyCleanupTopic(item)
	page, err := app.ListTrashEntries("", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var entry TrashEntry
	for _, candidate := range page.Items {
		if candidate.ID == item.ID {
			entry = candidate
		}
	}
	if entry.RecoveryEntryID == "" || !entry.CanPurge || entry.Ref != nil {
		t.Fatalf("placeholder entry = %+v", entry)
	}
	request := SessionLifecycleRequest{OperationID: "purge-placeholder", Action: "purge", ExpectedGeneration: page.Generation,
		Targets: []SessionLifecycleTarget{{WorkspaceID: item.WorkspaceID, RecoveryEntryID: entry.RecoveryEntryID}}}
	result, err := app.ApplySessionLifecycle(request)
	if err != nil || !result.Committed {
		t.Fatalf("purge placeholder = %+v, %v", result, err)
	}
	got := cleanupCandidate(t, app, item.ID)
	if got.Phase != "purged" || !got.Restored || got.Topic != nil {
		t.Fatalf("purged candidate = %+v", got)
	}
}

func TestLegacyCleanupDefaultTitleSetIsExact(t *testing.T) {
	for _, title := range []string{"", "  ", "新的会话", "新的會話", "New session", "新建会话", "新建會話", "新会话"} {
		if !isDefaultTopicTitle(title) {
			t.Fatalf("default title %q did not match", title)
		}
	}
	for _, title := range []string{"新建会话（2）", "New session 2", "prefix 新的会话", "新的会话 suffix", "My session"} {
		if isDefaultTopicTitle(title) {
			t.Fatalf("custom title %q matched", title)
		}
	}
}

func TestLegacyCleanupCanonicalDurableEvidenceIsConservative(t *testing.T) {
	app, workspaceRoot := newLegacyCleanupTestApp(t)
	tests := []struct {
		name  string
		event session.Event
		op    string
		want  string
	}{
		{
			name: "accepted submission without visible message",
			event: func() session.Event {
				body, _ := json.Marshal(session.SubmissionReceipt{SessionID: "accepted", SubmissionID: "send", Fingerprint: "fingerprint", TurnID: "turn"})
				return session.Event{Kind: "submission/accepted", Optional: true, Payload: body}
			}(),
			op: "accepted", want: "has_content",
		},
		{name: "explicit model", event: session.Event{Kind: "session/config", Payload: json.RawMessage(`{"modelRef":"provider/model"}`)}, op: "session-model:explicit", want: "has_content"},
		{name: "unknown optional event", event: session.Event{Kind: "future/optional", Optional: true, Payload: json.RawMessage(`{}`)}, op: "future", want: "unknown"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id := "evidence-" + string(rune('a'+index))
			ref, _ := createLegacyCleanupSession(t, app, workspaceRoot, id, false)
			binding, err := app.desktopSessionService("").Open(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			runtime := binding.Runtime()
			if test.event.Kind == "submission/accepted" {
				body, _ := json.Marshal(session.SubmissionReceipt{SessionID: id, SubmissionID: "send", Fingerprint: "fingerprint", TurnID: "turn", MessageID: "message"})
				test.event.Payload = body
			}
			if _, err := runtime.Session().Append(t.Context(), session.Batch{OperationID: test.op, TurnID: "turn", Events: []session.Event{test.event}}); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.Session().Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			info, err := app.desktopSessionService("").Query().Stat(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := binding.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got, _ := canonicalSessionDurableEvidence(t.Context(), info); got != test.want {
				t.Fatalf("classification = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLegacyCleanupCanonicalPinnedContextIsContent(t *testing.T) {
	app, workspaceRoot := newLegacyCleanupTestApp(t)
	ref, _ := createLegacyCleanupSession(t, app, workspaceRoot, "pinned-context", false)
	info, err := app.desktopSessionService("").Query().Stat(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePinnedContextState(info.Path, []string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "session:"+ref.SessionID)
	app.processLegacyCleanupSession(item)
	if got := cleanupCandidate(t, app, item.ID); got.Phase != "has_content" || got.Reason != "pinned_context" {
		t.Fatalf("candidate = %+v", got)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Active {
		t.Fatal("canonical session with pinned context was archived")
	}
}

func writeLegacyCleanupSource(t *testing.T, path string) string {
	t.Helper()
	legacy := agent.NewSession("system only")
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now(), Scope: "global", TopicID: "legacy-empty-topic", TopicTitle: defaultTopicTitle}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := legacyCleanupSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func TestLegacyCleanupSourceChecksCompleteArtifacts(t *testing.T) {
	dir := t.TempDir()
	t.Run("system only", func(t *testing.T) {
		path := filepath.Join(dir, "system-only.jsonl")
		fingerprint := writeLegacyCleanupSource(t, path)
		if got, reason, _ := classifyLegacyCleanupSource(path, "", fingerprint); got != "empty" {
			t.Fatalf("classification = %q (%s)", got, reason)
		}
	})
	t.Run("unreadable event log", func(t *testing.T) {
		path := filepath.Join(dir, "event-log.jsonl")
		writeLegacyCleanupSource(t, path)
		if err := os.WriteFile(store.SessionEventLog(path), []byte("durable execution\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fingerprint, err := legacyCleanupSourceFingerprint(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, _, _ := classifyLegacyCleanupSource(path, "", fingerprint); got != "unknown" {
			t.Fatalf("classification = %q", got)
		}
	})
	t.Run("unclassified context", func(t *testing.T) {
		path := filepath.Join(dir, "context.jsonl")
		writeLegacyCleanupSource(t, path)
		if err := os.WriteFile(store.SessionContext(path), []byte(`{"future":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		fingerprint, err := legacyCleanupSourceFingerprint(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, _, _ := classifyLegacyCleanupSource(path, "", fingerprint); got != "unknown" {
			t.Fatalf("classification = %q", got)
		}
	})
	t.Run("empty derived checkpoints", func(t *testing.T) {
		path := filepath.Join(dir, "empty-checkpoints.jsonl")
		writeLegacyCleanupSource(t, path)
		if err := os.WriteFile(store.SessionTranscriptProjection(path), []byte(`{"version":1,"identity":{},"records":[],"runtime":{"pendingEvents":[]},"activeAttempts":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.SessionContext(path), []byte(`{"schema_version":4,"projection":{"messages":[]}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		fingerprint, err := legacyCleanupSourceFingerprint(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, reason, _ := classifyLegacyCleanupSource(path, "", fingerprint); got != "empty" {
			t.Fatalf("classification = %q (%s)", got, reason)
		}
	})
}

func TestLegacyCleanupMigratedSourceArchivesOnlyMappedSession(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	path := filepath.Join(config.SessionDir(), "legacy-empty-source.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyCleanupSource(t, path)
	if err := ensureTopicIndexed("global", "", "legacy-empty-topic", defaultTopicTitle, topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	cleanupState, err := app.legacyCleanup.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var item legacycleanup.Candidate
	for _, candidate := range cleanupState.Items {
		if candidate.Kind == "legacy" && sameDesktopPath(candidate.SourcePath, path) {
			item = candidate
			break
		}
	}
	if item.ID == "" {
		t.Fatalf("legacy candidate missing from %+v", cleanupState.Items)
	}
	if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{root: filepath.Dir(path), scope: "global", headID: item.SourceHeadID}, workspaceID); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping := state.SourceMappings[desktopSourceKey(path, item.SourceHeadID)]
	if mapping.SessionID == "" {
		t.Fatalf("missing source mapping: %+v", state.SourceMappings)
	}
	bound := cleanupCandidate(t, app, item.ID)
	if bound.SessionID != mapping.SessionID || bound.EventSequence == 0 {
		t.Fatalf("migration did not freeze canonical binding: %+v", bound)
	}
	app.processLegacyCleanupSource(bound)
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[mapping.SessionID].Lifecycle != workspacestate.Archived {
		t.Fatalf("mapped session lifecycle = %q", state.SessionStates[mapping.SessionID].Lifecycle)
	}
	if got := cleanupCandidate(t, app, item.ID); got.SessionID != mapping.SessionID || got.Phase != "archived" {
		t.Fatalf("cleanup candidate = %+v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("legacy source was removed: %v", err)
	}
}

func TestLegacyCleanupMigratedSourceRejectsSameTitleRewriteAfterBinding(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	path := filepath.Join(config.SessionDir(), "legacy-title-rewrite.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyCleanupSource(t, path)
	if err := ensureTopicIndexed("global", "", "legacy-empty-topic", defaultTopicTitle, topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	cleanupState, err := app.legacyCleanup.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var item legacycleanup.Candidate
	for _, candidate := range cleanupState.Items {
		if candidate.Kind == "legacy" && sameDesktopPath(candidate.SourcePath, path) {
			item = candidate
			break
		}
	}
	if item.ID == "" {
		t.Fatal("legacy candidate missing")
	}
	if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{root: filepath.Dir(path), scope: "global", headID: item.SourceHeadID}, workspaceID); err != nil {
		t.Fatal(err)
	}
	bound := cleanupCandidate(t, app, item.ID)
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: bound.SessionID}
	if err := app.desktopSessionService("").SetTitle(t.Context(), ref, defaultTopicTitle); err != nil {
		t.Fatal(err)
	}
	app.processLegacyCleanupSource(bound)
	if got := cleanupCandidate(t, app, item.ID); got.Phase != "protected" || got.Reason != "title_or_content_changed" {
		t.Fatalf("same-title rewrite candidate = %+v", got)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[bound.SessionID].Lifecycle != workspacestate.Active {
		t.Fatal("same-title rewrite session was archived")
	}
}

func TestLegacyCleanupAlreadyMappedSessionKeepsLegacySidecarContent(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	path := filepath.Join(config.SessionDir(), "legacy-mapped-sidecar.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyCleanupSource(t, path)
	if err := ensureTopicIndexed("global", "", "legacy-empty-topic", defaultTopicTitle, topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	heads, err := session.LegacyMigrationHeads(t.Context(), path)
	if err != nil || len(heads) != 1 {
		t.Fatalf("legacy heads = %+v, %v", heads, err)
	}
	headID := heads[0].ID
	if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{root: filepath.Dir(path), scope: "global", headID: headID}, workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.SessionRecoveryState(path), []byte(`{"phase":"recoverable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping := state.SourceMappings[desktopSourceKey(path, headID)]
	if mapping.SessionID == "" {
		t.Fatalf("missing mapping: %+v", state.SourceMappings)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "session:"+mapping.SessionID)
	if len(item.Sources) != 1 || item.Sources[0].HeadID != headID {
		t.Fatalf("frozen sources = %+v", item.Sources)
	}
	app.processLegacyCleanupSession(item)
	if got := cleanupCandidate(t, app, item.ID); got.Phase != "has_content" || got.Reason != "recovery_state" {
		t.Fatalf("candidate = %+v", got)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[mapping.SessionID].Lifecycle != workspacestate.Active {
		t.Fatal("mapped session with legacy recovery state was archived")
	}
}

func TestLegacyCleanupTopicFinalFenceRejectsConcurrentRename(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	if _, err := app.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil {
		t.Fatal(err)
	}
	topic, err := app.CreateTopic("global", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "topic:"+topic.ID)
	app.legacyCleanupWorker.beforeArchive = func() { app.protectLegacyCleanupTopicMutation(topic.ID) }
	app.processLegacyCleanupTopic(item)
	if !topicIndexedInRegistry("global", "", topic.ID) {
		t.Fatal("topic was removed after a concurrent title mutation")
	}
	if got := cleanupCandidate(t, app, item.ID); got.Phase != "protected" || got.Reason != "title_mutated" {
		t.Fatalf("candidate = %+v", got)
	}
}

func TestLegacyCleanupTopicFinalFenceRejectsSameTitleMetadataRewrite(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	if _, err := app.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil {
		t.Fatal(err)
	}
	topic, err := app.CreateTopic("global", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "topic:"+topic.ID)
	if item.Topic == nil || item.Topic.RowRevision == 0 {
		t.Fatalf("candidate did not freeze topic row revision: %+v", item)
	}
	app.legacyCleanupWorker.beforeArchive = func() {
		if rewriteErr := setTopicTitleWithSource("", topic.ID, defaultTopicTitle, topicTitleSourceManual); rewriteErr != nil {
			t.Errorf("rewrite title: %v", rewriteErr)
		}
	}
	app.processLegacyCleanupTopic(item)
	if !topicIndexedInRegistry("global", "", topic.ID) {
		t.Fatal("topic was removed after a same-title metadata rewrite")
	}
	got := cleanupCandidate(t, app, item.ID)
	if got.Phase != "protected" || got.Reason != "title_mutated" {
		t.Fatalf("candidate = %+v", got)
	}
}

func TestLegacyCleanupTopicKeepsPlaceholderWhenWorkspaceIsUnavailable(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	workspaceRoot := filepath.Join(t.TempDir(), "offline-project")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ensureDesktopWorkspace(t.Context(), "project", workspaceRoot); err != nil {
		t.Fatal(err)
	}
	topic, err := app.CreateTopic("project", workspaceRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(workspaceRoot); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "topic:"+topic.ID)
	app.processLegacyCleanupTopic(item)
	got := cleanupCandidate(t, app, item.ID)
	if got.Phase != "unknown" || got.Reason != "workspace_unavailable" {
		t.Fatalf("unavailable workspace candidate = %+v", got)
	}
	if !topicIndexedInRegistry("project", workspaceRoot, topic.ID) {
		t.Fatal("placeholder from an unavailable workspace was removed")
	}
}

func TestLegacyCleanupTopicRestoreMergesOriginalOrganization(t *testing.T) {
	app, _ := newLegacyCleanupTestApp(t)
	if _, err := app.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil {
		t.Fatal(err)
	}
	before, err := app.CreateTopic("global", "", "Before")
	if err != nil {
		t.Fatal(err)
	}
	target, err := app.CreateTopic("global", "", "")
	if err != nil {
		t.Fatal(err)
	}
	after, err := app.CreateTopic("global", "", "After")
	if err != nil {
		t.Fatal(err)
	}
	if err := updateProjectsFile(func(file *desktopProjectFile) (bool, error) {
		file.GlobalTopics = []string{before.ID, target.ID, after.ID}
		file.GlobalPinnedTopics = []string{target.ID}
		file.GlobalGroups = []desktopGroup{{ID: "group", Title: "Group", TopicIDs: []string{before.ID, target.ID}}}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	item := cleanupCandidate(t, app, "topic:"+target.ID)
	app.processLegacyCleanupTopic(item)
	concurrent, err := app.CreateTopic("global", "", "Concurrent")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.restoreLegacyCleanupTopic(item.ID, item.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	file := loadProjectsFile()
	if slices.Index(file.GlobalTopics, target.ID) != item.Topic.Order || !containsDesktopString(file.GlobalPinnedTopics, target.ID) {
		t.Fatalf("restored organization = %+v", file)
	}
	if !containsDesktopString(file.GlobalTopics, concurrent.ID) {
		t.Fatal("restore overwrote a concurrently added topic")
	}
	if len(file.GlobalGroups) != 1 || slices.Index(file.GlobalGroups[0].TopicIDs, target.ID) != item.Topic.GroupOrder {
		t.Fatalf("restored group = %+v", file.GlobalGroups)
	}
}
