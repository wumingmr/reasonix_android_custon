package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/fileutil"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// reentrantSnapshotSink re-enters ContextMaintenanceSnapshot on every emit,
// which takes compactionMu. commitSummaryProjection must unlock before Emit.
type reentrantSnapshotSink struct {
	agent *Agent
	mu    sync.Mutex
	n     int
}

type modelContextRecorderStub struct {
	result SessionModelContextCommitResult
	err    error
	commit SessionModelContextCommit
}

type modelContextRecorderStep struct {
	result SessionModelContextCommitResult
	err    error
}

type sequencedModelContextRecorder struct {
	steps   []modelContextRecorderStep
	commits []SessionModelContextCommit
}

func (*modelContextRecorderStub) CheckpointSession(context.Context, SessionCheckpointBoundary) error {
	return nil
}

func (r *modelContextRecorderStub) RecordSessionModelContext(_ context.Context, commit SessionModelContextCommit) (SessionModelContextCommitResult, error) {
	r.commit = commit
	return r.result, r.err
}

func (*sequencedModelContextRecorder) CheckpointSession(context.Context, SessionCheckpointBoundary) error {
	return nil
}

func (r *sequencedModelContextRecorder) RecordSessionModelContext(_ context.Context, commit SessionModelContextCommit) (SessionModelContextCommitResult, error) {
	r.commits = append(r.commits, cloneSessionModelContextCommit(commit))
	if len(r.steps) == 0 {
		return SessionModelContextCommitResult{}, errors.New("unexpected model context commit")
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	return step.result, step.err
}

func (s *reentrantSnapshotSink) Emit(e event.Event) {
	if e.Kind != event.ContextMaintenanceEvent {
		return
	}
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	if s.agent != nil {
		_ = s.agent.ContextMaintenanceSnapshot()
	}
}

func TestCommitSummaryEmitsOutsideCompactionLock(t *testing.T) {
	prov := &fakeProvider{reply: "digest for reentrant emit"}
	sess := &Session{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("work line\n", 800)},
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("more work\n", 800)},
		{Role: provider.RoleUser, Content: "tail"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	sink := &reentrantSnapshotSink{}
	a := New(prov, tool.NewRegistry(), sess, Options{
		ContextWindow: 20_000, CompactRatio: 0.5, RecentKeep: 2,
		SessionPath: path, WorkspaceID: "ws", ModelRef: "p/m",
	}, sink)
	sink.agent = a
	if err := a.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("CompactNow: %v", err)
	}
	sink.mu.Lock()
	n := sink.n
	sink.mu.Unlock()
	if n == 0 {
		t.Fatal("expected context_maintenance emit after checkpoint install")
	}
	if got := a.currentProjectionVersion(); got != 1 {
		t.Fatalf("projection version = %d, want 1", got)
	}
}

func TestCommitSummaryRetainsAcceptedProjectionOnDurabilityFailure(t *testing.T) {
	recorder := &modelContextRecorderStub{
		result: SessionModelContextCommitResult{Accepted: true},
		err:    errors.New("injected flush failure"),
	}
	a := New(&fakeProvider{reply: "durability failure digest"}, tool.NewRegistry(), &Session{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("work line\n", 800)},
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("more work\n", 800)},
		{Role: provider.RoleUser, Content: "tail"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}, Options{ContextWindow: 20_000, CompactRatio: 0.5, RecentKeep: 2, SessionCheckpointer: recorder}, event.Discard)

	if err := a.CompactNow(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "injected flush failure") {
		t.Fatalf("CompactNow error = %v, want durability failure", err)
	}
	if got := a.currentProjectionVersion(); got != 1 {
		t.Fatalf("accepted projection version = %d, want 1", got)
	}
	if len(recorder.commit.Messages) == 0 || recorder.commit.OperationID == "" {
		t.Fatalf("recorder received incomplete commit: %+v", recorder.commit)
	}
	if a.sess.checkpointState != "pending" {
		t.Fatalf("checkpoint state = %q, want pending", a.sess.checkpointState)
	}
}

func TestPendingProjectionBlocksModelUntilExactCommitIsDurable(t *testing.T) {
	recorder := &sequencedModelContextRecorder{steps: []modelContextRecorderStep{
		{result: SessionModelContextCommitResult{Accepted: true}, err: errors.New("initial flush failure")},
		{result: SessionModelContextCommitResult{Accepted: true}, err: errors.New("retry flush failure")},
		{result: SessionModelContextCommitResult{Accepted: true, Durable: true}},
	}}
	prov := &scriptedProvider{name: "model", turns: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "durable retry digest"}, {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "continued"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, tool.NewRegistry(), &Session{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("work line\n", 800)},
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("more work\n", 800)},
		{Role: provider.RoleUser, Content: "tail"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}, Options{ContextWindow: 20_000, CompactRatio: 0.5, RecentKeep: 2, SessionCheckpointer: recorder}, event.Discard)

	if err := a.CompactNow(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "initial flush failure") {
		t.Fatalf("CompactNow error = %v, want initial durability failure", err)
	}
	if prov.call != 1 {
		t.Fatalf("provider calls after summary = %d, want 1", prov.call)
	}
	if err := a.Run(t.Context(), "must stay blocked"); err == nil || !strings.Contains(err.Error(), "retry flush failure") {
		t.Fatalf("blocked Run error = %v, want retry durability failure", err)
	}
	if prov.call != 1 {
		t.Fatalf("provider dispatched with pending durability: calls=%d, want 1", prov.call)
	}
	if err := a.Run(t.Context(), "continue after durable"); err != nil {
		t.Fatalf("Run after durable retry: %v", err)
	}
	if prov.call != 2 {
		t.Fatalf("provider calls after recovery = %d, want 2", prov.call)
	}
	if len(recorder.commits) != 3 {
		t.Fatalf("model context commits = %d, want 3", len(recorder.commits))
	}
	for i := 1; i < len(recorder.commits); i++ {
		if !reflect.DeepEqual(recorder.commits[0], recorder.commits[i]) {
			t.Fatalf("retry %d changed accepted commit\nfirst=%+v\nretry=%+v", i, recorder.commits[0], recorder.commits[i])
		}
	}
	if a.sess.pendingModelContextCommit != nil || a.sess.checkpointState != "applied" {
		t.Fatalf("pending checkpoint not cleared: pending=%+v state=%q", a.sess.pendingModelContextCommit, a.sess.checkpointState)
	}
}

func TestCommitSummaryRollsBackRejectedProjection(t *testing.T) {
	recorder := &modelContextRecorderStub{err: errors.New("injected prepare failure")}
	a := New(&fakeProvider{reply: "rejected projection digest"}, tool.NewRegistry(), &Session{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("work line\n", 800)},
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("more work\n", 800)},
		{Role: provider.RoleUser, Content: "tail"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}, Options{ContextWindow: 20_000, CompactRatio: 0.5, RecentKeep: 2, SessionCheckpointer: recorder}, event.Discard)

	if err := a.CompactNow(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "injected prepare failure") {
		t.Fatalf("CompactNow error = %v, want prepare failure", err)
	}
	if got := a.currentProjectionVersion(); got != 0 {
		t.Fatalf("rejected projection version = %d, want 0", got)
	}
}

// TestCommitSurvivesPostPublishDirSyncFailure locks the publish contract:
// after rename the checkpoint is committed. A parent-dir fsync failure must
// not roll back in-memory generation/projection (memory/disk fork).
func TestCommitSurvivesPostPublishDirSyncFailure(t *testing.T) {
	restore := fileutil.SetSyncParentDirForTest(func(string) error {
		return errors.New("injected parent dir fsync failure")
	})
	t.Cleanup(restore)

	prov := &fakeProvider{reply: "digest after dir-sync fault"}
	sess := &Session{Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("work line\n", 800)},
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("more work\n", 800)},
		{Role: provider.RoleUser, Content: "tail"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	a := New(prov, tool.NewRegistry(), sess, Options{
		ContextWindow: 20_000, CompactRatio: 0.5, RecentKeep: 2,
		SessionPath: path, WorkspaceID: "ws", ModelRef: "p/m",
	}, event.Discard)
	if err := a.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("CompactNow with post-publish dir sync fault: %v", err)
	}
	memVer := a.currentProjectionVersion()
	if memVer != 1 {
		t.Fatalf("memory projection version = %d, want 1", memVer)
	}
	disk, ok, err := LoadCompactionState(path)
	if err != nil || !ok {
		t.Fatalf("load disk checkpoint: ok=%v err=%v", ok, err)
	}
	if disk.Projection.ProjectionVersion != memVer {
		t.Fatalf("disk/memory fork: disk=%d mem=%d", disk.Projection.ProjectionVersion, memVer)
	}
	if disk.Generation != a.sess.compactionState.Generation {
		t.Fatalf("generation fork: disk=%d mem=%d", disk.Generation, a.sess.compactionState.Generation)
	}
}

// TestBlockedReceiptSurvivesPostPublishDirSyncFailure ensures a failed summary
// still installs the generation-scoped receipt in memory when only parent-dir
// fsync fails after rename — otherwise the next Prepare pays for another summary.
func TestBlockedReceiptSurvivesPostPublishDirSyncFailure(t *testing.T) {
	restore := fileutil.SetSyncParentDirForTest(func(string) error {
		return errors.New("injected parent dir fsync failure")
	})
	t.Cleanup(restore)

	const window = 10_000
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("old work ", 500)},
		{Role: provider.RoleUser, Content: "current"},
		{Role: provider.RoleAssistant, Content: "tail"},
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prov := &failingSummaryProvider{}
	a := New(prov, tool.NewRegistry(), &Session{Messages: append([]provider.Message(nil), messages...)}, Options{
		ContextWindow: window, CompactRatio: 0.85, RecentKeep: 2,
		WorkspaceID: "workspace", ModelRef: "model",
	}, event.Discard)
	a.BindSessionPath(path, true)

	policy := ContextPreparePolicy{Trigger: CompactionTriggerPressure, ObservedInputTokens: 8600}
	if _, err := a.contextManager().Prepare(context.Background(), policy); err != nil {
		t.Fatalf("above-ratio failure should not reject: %v", err)
	}
	if prov.calls != 1 {
		t.Fatalf("summary calls = %d, want 1", prov.calls)
	}
	if a.sess.compactionState.LastReceipt == nil {
		t.Fatal("memory lost blocked/failed receipt after post-publish dir-sync fault")
	}
	if status := a.sess.compactionState.LastReceipt.Status; status != "blocked" && status != "failed" {
		t.Fatalf("receipt status = %q", status)
	}
	disk, ok, err := LoadCompactionState(path)
	if err != nil || !ok || disk.LastReceipt == nil {
		t.Fatalf("disk receipt missing: ok=%v err=%v", ok, err)
	}
	if disk.Generation != a.sess.compactionState.Generation {
		t.Fatalf("blocked generation fork: disk=%d mem=%d", disk.Generation, a.sess.compactionState.Generation)
	}
	if _, err := a.contextManager().Prepare(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if prov.calls != 1 {
		t.Fatalf("same generation re-summarized after dir-sync fault: calls=%d", prov.calls)
	}
}

func TestLoadProjectionSidecarDoesNotRewriteExactKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "u"},
	}
	hash := coveredPrefixHash(msgs, len(msgs))
	key := promptCacheKey("ws", BranchID(path), "p/m")
	st := CompactionState{
		SchemaVersion:     compactionStateSchemaCurrent,
		TranscriptVersion: 0,
		PromptCacheKey:    key,
		Projection: ContextProjection{
			Messages: msgs, CoveredCount: len(msgs), CoveredPrefixHash: hash,
			ProjectionVersion: 3, TranscriptVersion: 0,
		},
		UpdatedAt: time.Now().UTC(),
	}
	if err := SaveCompactionState(path, st); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ContextStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, tool.NewRegistry(), &Session{Messages: append([]provider.Message(nil), msgs...)}, Options{
		SessionPath: path, WorkspaceID: "ws", ModelRef: "p/m",
	}, event.Discard)
	a.LoadProjectionSidecar(path)
	if a.currentProjectionVersion() != 3 {
		t.Fatalf("version = %d, want 3", a.currentProjectionVersion())
	}
	after, err := os.ReadFile(ContextStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("exact-key restore rewrote sidecar (%d -> %d bytes)", len(before), len(after))
	}
}

func TestSaveCompactionStateStripsLegacyWriterFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	st := CompactionState{
		SchemaVersion:     compactionStateSchemaCurrent,
		TranscriptVersion: 1,
		PromptCacheKey:    "k",
		LastTrigger:       CompactionTriggerPressure,
		LastMode:          CompactionModeSummarized,
		LastSourceTokens:  1000,
		LastResultTokens:  200,
		BlockedInputHash:  "legacy-blocked",
		BlockedReason:     "legacy",
		LastReceipt: &ContextMaintenanceReceipt{
			Status: "applied", Action: "summary", ProjectionVersion: 1,
			InputHash: "in", OutputHash: "out",
		},
	}
	if err := SaveCompactionState(path, st); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ContextStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{
		`"last_trigger"`, `"last_mode"`, `"last_source_tokens"`,
		`"last_result_tokens"`, `"blocked_input_hash"`, `"blocked_reason"`,
	} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("new writer re-emitted %s:\n%s", banned, raw)
		}
	}
	got, ok, err := LoadCompactionState(path)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.LastMode != "" || got.LastTrigger != "" || got.BlockedInputHash != "" {
		t.Fatalf("legacy mirrors present after save: %+v", got)
	}
	if got.LastReceipt == nil || got.LastReceipt.Status != "applied" {
		t.Fatalf("receipt lost: %+v", got.LastReceipt)
	}
}

func TestLoadProjectionSidecarNormalizesNativeKeyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "u"},
	}
	hash := coveredPrefixHash(msgs, len(msgs))
	key := promptCacheKey("ws", BranchID(path), "p/m")
	st := CompactionState{
		SchemaVersion:     compactionStateSchemaCurrent,
		TranscriptVersion: 0,
		PromptCacheKey:    key + "|context-editing-native-anthropic",
		Projection: ContextProjection{
			Messages: msgs, CoveredCount: len(msgs), CoveredPrefixHash: hash,
			ProjectionVersion: 2, TranscriptVersion: 0,
		},
		UpdatedAt: time.Now().UTC(),
	}
	if err := SaveCompactionState(path, st); err != nil {
		t.Fatal(err)
	}
	a := New(nil, tool.NewRegistry(), &Session{Messages: append([]provider.Message(nil), msgs...)}, Options{
		SessionPath: path, WorkspaceID: "ws", ModelRef: "p/m",
	}, event.Discard)
	a.LoadProjectionSidecar(path)
	if a.currentProjectionVersion() != 2 {
		t.Fatalf("version = %d, want 2", a.currentProjectionVersion())
	}
	loaded, ok, err := LoadCompactionState(path)
	if err != nil || !ok {
		t.Fatalf("reload: ok=%v err=%v", ok, err)
	}
	if loaded.PromptCacheKey != key {
		t.Fatalf("PromptCacheKey = %q, want normalized %q", loaded.PromptCacheKey, key)
	}
	before, err := os.ReadFile(ContextStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	a2 := New(nil, tool.NewRegistry(), &Session{Messages: append([]provider.Message(nil), msgs...)}, Options{
		SessionPath: path, WorkspaceID: "ws", ModelRef: "p/m",
	}, event.Discard)
	a2.LoadProjectionSidecar(path)
	after, err := os.ReadFile(ContextStatePath(path))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("second restore rewrote already-normalized sidecar")
	}
}
