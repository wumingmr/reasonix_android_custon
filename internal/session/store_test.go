package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/filelock"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

func TestStoredManifestRequiresExactFormatBoundary(t *testing.T) {
	t.Parallel()
	if !supportedStoredManifest(Manifest{SchemaVersion: SchemaVersion, Codec: Codec}) {
		t.Fatal("in-place v4 session was rejected")
	}
	if !supportedStoredManifest(Manifest{SchemaVersion: SchemaVersion, Codec: Codec, StorageRevision: StorageRevision}) {
		t.Fatal("final v4 manifest was rejected")
	}
	if !supportedStoredManifest(Manifest{SchemaVersion: 3, Codec: FinalV31Codec}) {
		t.Fatal("frozen v3.1 input format was rejected")
	}
	if supportedStoredManifest(Manifest{SchemaVersion: SchemaVersion, Codec: FinalV31Codec}) {
		t.Fatal("schema-4 data mislabeled as v3.1 was accepted")
	}
}

func TestLegacyLinearStoreOpensAndAppendsWithoutCodecUpgrade(t *testing.T) {
	for _, codec := range []string{LegacyLinearCodec, FinalV31Codec, PrototypeCodec} {
		t.Run(codec, func(t *testing.T) { testNativeLinearStore(t, codec) })
	}
}

func testNativeLinearStore(t *testing.T, codec string) {
	root := t.TempDir()
	dir := filepath.Join(root, "legacy-linear")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: 3, Codec: codec, SessionID: "legacy-linear", CreatedAt: time.Now().UTC(), WriterGeneration: 1}
	if err := writeManifestFile(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyLogName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenWithOptions(dir, "legacy-linear", OpenOptions{ExternalHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	_, err = s.Append(t.Context(), Batch{OperationID: "continue-old", Events: []Event{{Kind: "turn/start"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := readStoredManifest(filepath.Join(dir, "manifest.json"))
	if err != nil || got.Codec != codec || got.SchemaVersion != 3 {
		t.Fatalf("manifest = %+v, err=%v", got, err)
	}
	commits, err := Replay(dir, nil)
	if err != nil || len(commits) != 1 || commits[0].Codec != codec {
		t.Fatalf("commits = %+v, err=%v", commits, err)
	}
	// Preserve the byte boundary of records produced by a different JSON
	// encoder. Re-marshalling a record to derive its end would truncate this.
	logPath := filepath.Join(dir, legacyLogName)
	original, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	original = append([]byte("  "), original...)
	if err := os.WriteFile(logPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(dir, "legacy-linear", OpenOptions{ExternalHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(logPath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("opening changed original log bytes: %v", err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "finish-old", Events: []Event{{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	commits, err = Replay(dir, nil)
	if err != nil || len(commits) != 2 || commits[1].FirstSequence != 2 {
		t.Fatalf("reopened commits = %+v, err=%v", commits, err)
	}
	complete, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, append(append([]byte(nil), complete...), []byte(`{"partial":`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(dir, "legacy-linear", OpenOptions{ExternalHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(logPath); err != nil || !bytes.Equal(got, complete) {
		t.Fatalf("torn repair changed complete prefix: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "events.torn-*.tail"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("torn evidence missing: %v %v", backups, err)
	}
	if got, err := os.ReadFile(backups[0]); err != nil || string(got) != `{"partial":` {
		t.Fatalf("torn evidence changed: %q %v", got, err)
	}
}

func TestNativePrototypeHistoryRewriteRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "prototype")
	payload, _ := json.Marshal(map[string]any{"messages": []provider.Message{{ID: "old", Role: provider.RoleUser, Content: "old"}}})
	writePrototypeStore(t, dir, []Event{{Kind: "context/replace", Payload: payload}}, "")
	s, err := Open(dir, "prototype")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if got := s.Snapshot().Projection.ModelMessages; len(got) != 1 || got[0].Content != "old" {
		t.Fatalf("old projection: %+v", got)
	}
	payload, _ = json.Marshal(map[string]any{"messages": []provider.Message{{ID: "new", Role: provider.RoleUser, Content: "new"}}})
	if _, err := s.Append(t.Context(), Batch{OperationID: "rewrite", Events: []Event{{Kind: "history/replace", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, legacyLogName))
	if err != nil || bytes.Contains(raw, []byte(`"kind":"history/replace"`)) {
		t.Fatalf("prototype codec changed: %s %v", raw, err)
	}
	s, err = Open(dir, "prototype")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Projection.ModelMessages; len(got) != 1 || got[0].Content != "new" {
		t.Fatalf("reopened projection: %+v", got)
	}
}

func TestCommitBatchAndProjectionAreAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	todos, _ := json.Marshal(map[string]any{"todos": []event.Todo{{Content: "A", Status: "in_progress"}, {Content: "B", Status: "in_progress"}}})
	commit, err := s.Append(t.Context(), Batch{OperationID: "todo-1", TurnID: "turn-1", Events: []Event{
		{Kind: "turn/start"},
		{Kind: "todo/write", Payload: todos},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if commit.FirstSequence != 1 || commit.EventCount != 2 || commit.Events[1].Sequence != 2 {
		t.Fatalf("commit boundary = %+v", commit)
	}
	projection := s.Snapshot().Projection
	if !projection.TodoWritten || len(projection.Todos) != 2 || projection.Todos[1].Content != "B" {
		t.Fatalf("live todo projection = %+v", projection)
	}
	if _, err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	commits, err := Replay(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Project(commits)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.TodoWritten || len(replayed.Todos) != 2 || replayed.Todos[1].Content != "B" {
		t.Fatalf("replayed todo projection = %+v", replayed)
	}
}

func TestProjectionRebuildsMessagesGoalPlanInteractionsAndRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	message, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "m1", Role: provider.RoleUser, Content: "hello"}})
	interaction, _ := json.Marshal(map[string]any{"id": "p1", "state": "pending"})
	recovery, _ := json.Marshal(event.RecoveryStatus{State: "recovery_required", RequiresUserDecision: true, Reason: "worker did not exit"})
	if _, err := s.Append(t.Context(), Batch{OperationID: "state-1", TurnID: "t1", Events: []Event{
		{Kind: "turn/start"},
		{Kind: "message/complete", Payload: message},
		{Kind: "interaction/created", Payload: interaction},
		{Kind: "plan/state", Payload: json.RawMessage(`{"status":"approved"}`)},
		{Kind: "goal/state", Payload: json.RawMessage(`{"objective":"ship","status":"active"}`)},
		{Kind: "runtime/recovery", Payload: recovery},
	}}); err != nil {
		t.Fatal(err)
	}
	projection := s.Snapshot().Projection
	if len(projection.Messages) != 1 || projection.Messages[0].ID != "m1" || projection.Interactions["p1"] != "pending" {
		t.Fatalf("message/interaction projection = %+v", projection)
	}
	if projection.Recovery == nil || !projection.Recovery.RequiresUserDecision || len(projection.PlanState) == 0 || len(projection.GoalState) == 0 {
		t.Fatalf("state projection = %+v", projection)
	}
}

func TestModelContextReplaceDoesNotRewriteUIHistory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "replace")
	store, err := Open(dir, "replace")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	messages := []provider.Message{{ID: "u1", Role: provider.RoleUser, Content: "new context"}}
	payload, _ := json.Marshal(map[string]any{"messages": messages, "reason": "compaction", "sourceSequences": []uint64{1}})
	if _, err := store.Append(context.Background(), Batch{OperationID: "replace-1", Events: []Event{{Kind: "model/context-replace", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot().Projection.Messages
	if len(got) != 0 {
		t.Fatalf("messages = %#v", got)
	}
	derived := store.DeriveMessages()
	if len(derived) != 1 || derived[0].ID != "u1" {
		t.Fatalf("model messages = %#v", derived)
	}
}

func TestMessageUpsertPersistsLocalMetadataWithoutEnteringModelHistory(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "upsert"), "upsert")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	local := provider.Message{ID: "receipt", Role: provider.RoleTool, Name: provider.LocalOnlyToolName, ToolCallID: provider.LocalOnlyToolID, LocalOnly: true, ProtocolRecovery: json.RawMessage(`{"version":1,"id":"r","state":"pending"}`)}
	payload, _ := json.Marshal(map[string]any{"message": local})
	if _, err := store.Append(t.Context(), Batch{OperationID: "upsert-local", Events: []Event{{Kind: "message/upsert", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot().Projection
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].ID != "receipt" {
		t.Fatalf("ui messages = %#v", snapshot.Messages)
	}
	if len(snapshot.ModelMessages) != 0 {
		t.Fatalf("local receipt leaked into model history: %#v", snapshot.ModelMessages)
	}
	local.ProtocolRecovery = json.RawMessage(`{"version":1,"id":"r","state":"consumed"}`)
	payload, _ = json.Marshal(map[string]any{"message": local})
	if _, err := store.Append(t.Context(), Batch{OperationID: "upsert-local-again", Events: []Event{{Kind: "message/upsert", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Projection; len(got.Messages) != 1 || len(got.ModelMessages) != 0 {
		t.Fatalf("updated projections = %#v / %#v", got.Messages, got.ModelMessages)
	}
}

func TestMessageCompleteRejectsDuplicateStableIDAtomically(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "duplicates"), "duplicates")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "same", Role: provider.RoleUser, Content: "one"}})
	if _, err := store.Append(t.Context(), Batch{OperationID: "one", Events: []Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	before := store.Snapshot()
	payload, _ = json.Marshal(map[string]any{"message": provider.Message{ID: "same", Role: provider.RoleAssistant, Content: "two"}})
	if _, err := store.Append(t.Context(), Batch{OperationID: "two", Events: []Event{{Kind: "message/complete", Payload: payload}}}); err == nil {
		t.Fatal("duplicate stable id accepted")
	}
	after := store.Snapshot()
	if after.EventSequence != before.EventSequence || len(after.Projection.Messages) != 1 {
		t.Fatalf("failed batch mutated state: before=%+v after=%+v", before, after)
	}
}

func TestCompactionReplacesOnlyModelProjection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "compaction")
	store, err := Open(dir, "compaction")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	original := provider.Message{ID: "u1", Role: provider.RoleUser, Content: "original"}
	messagePayload, _ := json.Marshal(map[string]any{"message": original})
	projected := []provider.Message{{ID: "summary", Role: provider.RoleUser, Content: "summary"}}
	compactionPayload, _ := json.Marshal(map[string]any{"messages": projected, "trigger": "manual"})
	if _, err := store.Append(context.Background(), Batch{OperationID: "compact", Events: []Event{
		{Kind: "message/complete", Payload: messagePayload},
		{Kind: "compaction", Payload: compactionPayload},
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot().Projection
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].ID != "u1" {
		t.Fatalf("canonical messages changed: %#v", snapshot.Messages)
	}
	if len(snapshot.ModelMessages) != 1 || snapshot.ModelMessages[0].ID != "summary" {
		t.Fatalf("model projection = %#v", snapshot.ModelMessages)
	}
}

func TestRestartRecoveryClosesTurnToolsAndInteractionsWithoutReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recover")
	first, err := Open(dir, "recover")
	if err != nil {
		t.Fatal(err)
	}
	call := json.RawMessage(`{"id":"call-1","name":"bash"}`)
	interaction := json.RawMessage(`{"id":"approval-1","state":"pending"}`)
	if _, err := first.Append(t.Context(), Batch{OperationID: "open-turn", TurnID: "turn-1", Events: []Event{
		{Kind: "turn/start"}, {Kind: "tool/start", Payload: call}, {Kind: "interaction/created", Payload: interaction},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	second, err := Open(dir, "recover")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if _, recovered, err := second.RecoverInterrupted(t.Context()); err != nil || !recovered {
		t.Fatalf("RecoverInterrupted = recovered %v, err %v", recovered, err)
	}
	projection := second.Snapshot().Projection
	if projection.TurnID != "" || projection.TurnStatus != event.TurnInterrupted || len(projection.ActiveTools) != 0 || len(projection.Interactions) != 0 {
		t.Fatalf("recovered projection = %+v", projection)
	}
}

func TestAppendValidationDoesNotChangeLiveState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	tests := []Event{
		{Kind: "todo/write", Payload: json.RawMessage(`{"todos":[{"content":" A ","status":"pending"}]}`)},
		{Kind: "interaction/created", Payload: json.RawMessage(`{"id":"p","state":"answered"}`)},
		{Kind: "interaction/resolved", Payload: json.RawMessage(`{"id":"p","state":"pending"}`)},
		{Kind: "message/complete", Payload: json.RawMessage(`{"message":{"role":"user","content":"missing id"}}`)},
		{Kind: "plan/state", Payload: json.RawMessage(`[]`)},
	}
	for i, invalid := range tests {
		before := s.Snapshot()
		_, err := s.Append(t.Context(), Batch{OperationID: "invalid-" + string(rune('a'+i)), Events: []Event{invalid}})
		if !errors.Is(err, ErrDamagedStore) {
			t.Fatalf("event %s error = %v", invalid.Kind, err)
		}
		after := s.Snapshot()
		if after.EventSequence != before.EventSequence {
			t.Fatalf("invalid %s advanced sequence: before=%d after=%d", invalid.Kind, before.EventSequence, after.EventSequence)
		}
	}
}

func TestSessionConfigIsProjectedFromTheEventStream(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "config", Events: []Event{{Kind: "session/config", Payload: json.RawMessage(`{"modelRef":"provider/model","modelIdentity":"revision-1"}`)}}}); err != nil {
		t.Fatal(err)
	}
	if projection := s.Snapshot().Projection; projection.ModelRef != "provider/model" || projection.ModelIdentity != "revision-1" {
		t.Fatalf("live model selection = %q / %q", projection.ModelRef, projection.ModelIdentity)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	commits, err := Replay(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(commits)
	if err != nil {
		t.Fatal(err)
	}
	if projection.ModelRef != "provider/model" || projection.ModelIdentity != "revision-1" {
		t.Fatalf("replayed model selection = %q / %q", projection.ModelRef, projection.ModelIdentity)
	}
}

func TestReplayIgnoresTornTailAndWriterRecoversUnderLease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "turn", Events: []Event{{Kind: "turn/start"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, currentLogName)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"torn`)
	_ = file.Close()
	commits, err := Replay(dir, nil)
	if err != nil || len(commits) != 1 {
		t.Fatalf("Replay partial tail = %d, %v", len(commits), err)
	}
	reopened, err := Open(dir, "s")
	if err != nil {
		t.Fatalf("writer recovery: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "events.torn-*.tail"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("torn tail backups = %v, %v", backups, err)
	}
	tail, err := os.ReadFile(backups[0])
	if err != nil || string(tail) != `{"torn` {
		t.Fatalf("preserved tail = %q, %v", tail, err)
	}
	if _, err := reopened.Append(t.Context(), Batch{OperationID: "after-recovery", Events: []Event{{Kind: "diagnostic", Optional: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	commits, err = Replay(dir, nil)
	if err != nil || len(commits) != 2 {
		t.Fatalf("Replay recovered log = %d, %v", len(commits), err)
	}
}

func TestReplayRejectsUnknownRequiredEventAndAllowsOptionalDiagnostic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "unknown", Events: []Event{{Kind: "future/required"}}}); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("unknown required append error = %v", err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "diagnostic", Events: []Event{{Kind: "future/diagnostic", Optional: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if commits, err := Replay(dir, nil); err != nil || len(commits) != 1 {
		t.Fatalf("optional diagnostic replay = %d, %v", len(commits), err)
	}
}

func TestOpenUsesSingleWriterLeaseAndAdvancesGeneration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	first, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, "s"); !errors.Is(err, ErrWriterOwned) {
		t.Fatalf("second writer error = %v", err)
	}
	firstGeneration := first.manifest.WriterGeneration
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	second, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if second.manifest.WriterGeneration != firstGeneration+1 {
		t.Fatalf("writer generation = %d, want %d", second.manifest.WriterGeneration, firstGeneration+1)
	}
}

func TestMigrateLegacyIsIdempotentAndUsesFrozenArtifacts(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, "sessions")
	path := filepath.Join(legacyDir, "old.jsonl")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	session := agent.NewSession("sys")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "hello", ID: "user-stable-id"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := agent.SetBranchModelSelectionPreserveUpdated(path, "provider/model", "connection-revision"); err != nil {
		t.Fatal(err)
	}
	goal := `{"objective":"finish","status":"paused","token_budget":123,"todos":[{"content":"old","status":"in_progress"}],"auto_continue":true}`
	if err := os.WriteFile(store.SessionGoalState(path), []byte(goal), 0o600); err != nil {
		t.Fatal(err)
	}
	v3root := filepath.Join(root, "sessions-v4")
	first, err := MigrateLegacy(context.Background(), path, v3root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MigrateLegacy(context.Background(), path, v3root)
	if err != nil {
		t.Fatal(err)
	}
	if first.TargetID != second.TargetID || !second.Reused {
		t.Fatalf("migration idempotency: first=%+v second=%+v", first, second)
	}
	commits, err := Replay(first.TargetDir, nil)
	if err != nil || len(commits) == 0 {
		t.Fatalf("Replay migrated = %d, %v", len(commits), err)
	}
	projection, err := Project(commits)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Messages) != 2 || projection.Messages[1].ID != "user-stable-id" {
		t.Fatalf("migrated messages = %+v", projection.Messages)
	}
	if projection.ModelRef != "provider/model" || projection.ModelIdentity != "connection-revision" {
		t.Fatalf("migrated model selection = %q / %q", projection.ModelRef, projection.ModelIdentity)
	}
	if containsJSONKey(projection.GoalState, "todos") || containsJSONKey(projection.GoalState, "auto_continue") {
		t.Fatalf("migrated goal projection = %s", projection.GoalState)
	}
	legacyGoal := filepath.Join(first.TargetDir, "legacy", filepath.Base(store.SessionGoalState(path)))
	if string(mustRead(t, legacyGoal)) != goal {
		t.Fatal("raw goal sidecar was not preserved byte-for-byte")
	}
}

func TestMigrateLegacyBeyondFormer128MiBReplayLimit(t *testing.T) {
	if os.Getenv("REASONIX_LARGE_SESSION_TEST") != "1" {
		t.Skip("set REASONIX_LARGE_SESSION_TEST=1 to run the exact 134,308,416-byte regression")
	}
	const (
		totalBytes = int64(134_308_416)
		messages   = 8_192
	)
	root := t.TempDir()
	legacy := filepath.Join(root, "oversized.jsonl")
	file, err := os.OpenFile(legacy, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	prefixBytes := int64(len(fmt.Sprintf(`{"role":"user","id":"m-%08d","content":"`, 0)))
	suffix := []byte("\"}\n")
	contentBytes := totalBytes - int64(messages)*(prefixBytes+int64(len(suffix)))
	if contentBytes <= 0 {
		t.Fatal("invalid oversized fixture dimensions")
	}
	base, extra := contentBytes/int64(messages), contentBytes%int64(messages)
	chunk := bytes.Repeat([]byte{'x'}, 32<<10)
	for i := range messages {
		prefix := fmt.Sprintf(`{"role":"user","id":"m-%08d","content":"`, i)
		if _, err := file.WriteString(prefix); err != nil {
			t.Fatal(err)
		}
		n := base
		if int64(i) < extra {
			n++
		}
		for n > 0 {
			part := min(n, int64(len(chunk)))
			if _, err := file.Write(chunk[:part]); err != nil {
				t.Fatal(err)
			}
			n -= part
		}
		if _, err := file.Write(suffix); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(legacy)
	if err != nil || info.Size() != totalBytes {
		t.Fatalf("legacy fixture size = %d, %v", info.Size(), err)
	}
	targetRoot := filepath.Join(root, "sessions-v4")
	result, err := MigrateLegacy(t.Context(), legacy, targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source.Size != totalBytes || result.MessageNum != messages {
		t.Fatalf("migration result = %+v", result)
	}
	service, err := NewService("capacity", NewFilesystemPersistence(targetRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	binding, err := service.Open(t.Context(), SessionRef{HostID: "capacity", SessionID: result.TargetID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Runtime().Session().AppendBatch(t.Context(), "continued-after-oversized-migration", []Event{{Kind: "session/title", Payload: json.RawMessage(`{"title":"continued"}`)}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := binding.Runtime().Session().Flush(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.DurableSequence != messages+1 {
		t.Fatalf("continued durable sequence = %d, want %d", receipt.DurableSequence, messages+1)
	}
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateEmptyLegacyTranscriptProducesValidExplicitEmptyHistory(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "empty.jsonl")
	if err := os.WriteFile(legacy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := MigrateLegacy(t.Context(), legacy, filepath.Join(root, "sessions-v4"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(result.TargetDir, result.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	projection := store.Snapshot().Projection
	if len(projection.Messages) != 0 || len(projection.ModelMessages) != 0 {
		t.Fatalf("empty migration projection = messages %#v model %#v", projection.Messages, projection.ModelMessages)
	}
}

func TestMigrateLegacyRefusesActiveSourceLease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.jsonl")
	if err := agent.NewSession("sys").Save(path); err != nil {
		t.Fatal(err)
	}
	lease, err := agent.TryAcquireSessionLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := MigrateLegacy(t.Context(), path, filepath.Join(dir, "sessions-v4")); !errors.Is(err, agent.ErrSessionLeaseHeld) {
		t.Fatalf("migration with active source lease error = %v", err)
	}
}

func TestMigrationMapLeaseWaitIsContextCancellable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions-v4", "migration-map.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := acquireMigrationMapLease(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquireMigrationMapLease error = %v", err)
	}
}

func TestMigrateLegacyHeadsBecomeIndependentLinearSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.jsonl")
	session := agent.NewSession("sys")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "root question"})
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: "root answer"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	childHead, err := session.ForkHead(path, session.Snapshot()[2].ID, agent.HeadKindFork, "child")
	if err != nil {
		t.Fatal(err)
	}
	session.Add(provider.Message{Role: provider.RoleUser, Content: "child only"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	heads, err := agent.ListSessionHeads(path)
	if err != nil {
		t.Fatal(err)
	}
	rootHead := ""
	for _, head := range heads {
		if head.ID != childHead {
			rootHead = head.ID
			break
		}
	}
	if rootHead == "" {
		t.Fatalf("legacy root head missing: %+v", heads)
	}

	v3root := filepath.Join(dir, "sessions-v4")
	rootResult, err := MigrateLegacyHead(t.Context(), path, v3root, rootHead)
	if err != nil {
		t.Fatal(err)
	}
	childResult, err := MigrateLegacyHead(t.Context(), path, v3root, childHead)
	if err != nil {
		t.Fatal(err)
	}
	if rootResult.TargetID == childResult.TargetID {
		t.Fatal("distinct legacy heads reused one v3 target")
	}
	rootCommits, err := Replay(rootResult.TargetDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	childCommits, err := Replay(childResult.TargetDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	rootProjection, err := Project(rootCommits)
	if err != nil {
		t.Fatal(err)
	}
	childProjection, err := Project(childCommits)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootProjection.Messages) != 3 || len(childProjection.Messages) != 4 || childProjection.Messages[3].Content != "child only" {
		t.Fatalf("head projections root=%+v child=%+v", rootProjection.Messages, childProjection.Messages)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func containsJSONKey(raw []byte, key string) bool {
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	_, ok := value[key]
	return ok
}
