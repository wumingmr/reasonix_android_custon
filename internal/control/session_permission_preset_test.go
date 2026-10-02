package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestCanonicalPermissionPresetFollowsSessionIdentity(t *testing.T) {
	restoreSandbox := SetPresetSandboxForTest(true)
	t.Cleanup(restoreSandbox)
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Create(t.Context(), session.CreateOptions{SessionID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(t.Context(), session.CreateOptions{SessionID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: first, ExclusiveSession: true})
	c.EnableServeSessionPermissionPresets(true)
	initial := c.PermissionSnapshot()
	if initial.SessionID != "first" || initial.Preset != ToolApprovalWorkspaceWrite {
		t.Fatalf("initial snapshot = %+v", initial)
	}
	sameChoice, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalWorkspaceWrite, initial.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if sameChoice.Revision <= initial.Revision {
		t.Fatalf("explicit same-value choice did not invalidate stale revision: %+v", sameChoice)
	}
	firstChoice, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalDangerFullAccess, sameChoice.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if firstChoice.SessionID != "first" || firstChoice.Preset != ToolApprovalDangerFullAccess {
		t.Fatalf("first choice = %+v", firstChoice)
	}
	if _, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalReadOnly, initial.Revision); err == nil {
		t.Fatal("stale permission revision was accepted")
	}
	if _, err := c.OpenSession(t.Context(), second.Ref()); err != nil {
		t.Fatal(err)
	}
	other := c.PermissionSnapshot()
	if other.SessionID != "second" || other.Preset != ToolApprovalWorkspaceWrite || other.Revision <= firstChoice.Revision {
		t.Fatalf("new session inherited the first preset: %+v", other)
	}
	if _, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalReadOnly, other.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenSession(t.Context(), first.Ref()); err != nil {
		t.Fatal(err)
	}
	if got := c.PermissionSnapshot(); got.SessionID != "first" || got.Preset != ToolApprovalDangerFullAccess {
		t.Fatalf("first preset was not restored: %+v", got)
	}
	if _, err := c.OpenSession(t.Context(), second.Ref()); err != nil {
		t.Fatal(err)
	}
	if got := c.PermissionSnapshot(); got.SessionID != "second" || got.Preset != ToolApprovalReadOnly {
		t.Fatalf("second preset was not restored: %+v", got)
	}
	broken, err := service.Create(t.Context(), session.CreateOptions{SessionID: "broken"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Session().AppendBatch(t.Context(), "invalid-plan", []session.Event{{Kind: "plan/state", Payload: []byte(`{"enabled":"invalid"}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenSession(t.Context(), broken.Ref()); err == nil {
		t.Fatal("invalid session projection was opened")
	}
	if got := c.PermissionSnapshot(); got.SessionID != "second" || got.Preset != ToolApprovalReadOnly {
		t.Fatalf("failed switch changed the permission snapshot: %+v", got)
	}
}

func TestCanonicalComposerProfilePersistsLegacyPermissionAlias(t *testing.T) {
	restoreSandbox := SetPresetSandboxForTest(true)
	t.Cleanup(restoreSandbox)
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "composer"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	c.EnableServeSessionPermissionPresets(true)
	if _, err := c.ApplyComposerProfileAtDurable(t.Context(), false, "ask", "", c.PermissionSnapshot().Revision); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Session().ExplicitPermissionPreset(); got != ToolApprovalReadOnly {
		t.Fatalf("legacy composer alias was not persisted canonically: %q", got)
	}
}

func TestCanonicalPermissionDowngradeFailsClosedAfterFlushError(t *testing.T) {
	restoreSandbox := SetPresetSandboxForTest(true)
	t.Cleanup(restoreSandbox)
	var failSync atomic.Bool
	store, err := session.CreateWithOptions(filepath.Join(t.TempDir(), "session"), "sync-failure", session.OpenOptions{
		Sync: func(file *os.File) error {
			if failSync.Load() {
				return errors.New("injected sync failure")
			}
			return file.Sync()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("desktop", failingFlushPersistence{session: store})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "sync-failure"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	c.EnableServeSessionPermissionPresets(true)
	full, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalDangerFullAccess, c.PermissionSnapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	failSync.Store(true)
	after, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalReadOnly, full.Revision)
	if err == nil {
		t.Fatal("failed fsync returned a successful permission choice")
	}
	if after.Preset != ToolApprovalReadOnly || after.Revision <= full.Revision {
		t.Fatalf("failed downgrade left full access active: %+v", after)
	}
	rejected, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalDangerFullAccess, after.Revision)
	if err == nil || rejected.Preset != ToolApprovalReadOnly {
		t.Fatalf("failed upgrade exposed full access: snapshot=%+v err=%v", rejected, err)
	}
	if _, err := c.publishSessionRuntime(runtime, agent.NewSession("system"), false); err == nil {
		t.Fatal("a session with an unflushed broad preset was published")
	}
	if got := c.PermissionSnapshot().Preset; got != ToolApprovalReadOnly {
		t.Fatalf("failed publication changed the enforced preset to %q", got)
	}
	rebuilt := newOwnedTestController(t, Options{
		Executor: agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard),
		Sink:     event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true,
	})
	rebuilt.EnableServeSessionPermissionPresets(true)
	if got := rebuilt.PermissionSnapshot().Preset; got != ToolApprovalReadOnly {
		t.Fatalf("rebuilt controller exposed unflushed preset %q", got)
	}
}

func TestCanonicalPermissionSnapshotNeverMixesSessionAndPreset(t *testing.T) {
	restoreSandbox := SetPresetSandboxForTest(true)
	t.Cleanup(restoreSandbox)
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Create(t.Context(), session.CreateOptions{SessionID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(t.Context(), session.CreateOptions{SessionID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: first, ExclusiveSession: true})
	c.EnableServeSessionPermissionPresets(true)
	if _, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalDangerFullAccess, c.PermissionSnapshot().Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenSession(t.Context(), second.Ref()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalReadOnly, c.PermissionSnapshot().Revision); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	observed := make(chan error, 1)
	go func() {
		defer close(observed)
		for {
			select {
			case <-done:
				return
			default:
			}
			snapshot := c.PermissionSnapshot()
			if (snapshot.SessionID == "first" && snapshot.Preset != ToolApprovalDangerFullAccess) ||
				(snapshot.SessionID == "second" && snapshot.Preset != ToolApprovalReadOnly) {
				observed <- fmt.Errorf("mixed permission snapshot: %+v", snapshot)
				return
			}
		}
	}()
	for range 20 {
		if _, err := c.OpenSession(t.Context(), first.Ref()); err != nil {
			close(done)
			t.Fatal(err)
		}
		if _, err := c.OpenSession(t.Context(), second.Ref()); err != nil {
			close(done)
			t.Fatal(err)
		}
	}
	close(done)
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalPermissionPresetRestoresAfterServiceRestart(t *testing.T) {
	restoreSandbox := SetPresetSandboxForTest(true)
	t.Cleanup(restoreSandbox)
	root := filepath.Join(t.TempDir(), "sessions")
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "reopened"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := New(Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	c.EnableServeSessionPermissionPresets(true)
	before := c.PermissionSnapshot()
	if _, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalDangerFullAccess, before.Revision); err != nil {
		t.Fatal(err)
	}
	c.Close()
	<-c.closeFinalized
	if err := service.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := session.NewService("desktop", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.CloseAll(context.Background()) })
	binding, err := restarted.Open(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Release(context.Background())
	exec = agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	reopened := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: restarted, SessionRuntime: binding.Runtime(), ExclusiveSession: true})
	reopened.EnableServeSessionPermissionPresets(true)
	if got := reopened.PermissionSnapshot(); got.SessionID != "reopened" || got.Preset != ToolApprovalDangerFullAccess {
		t.Fatalf("restarted controller did not restore durable preset: %+v", got)
	}
}

func TestUnmanagedCanonicalSessionKeepsReadOnlyAcrossTransitions(t *testing.T) {
	restoreSandbox := SetPresetSandboxForTest(true)
	t.Cleanup(restoreSandbox)
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Create(t.Context(), session.CreateOptions{SessionID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	c := newOwnedTestController(t, Options{
		Executor: agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard),
		Sink:     event.Discard, SessionService: service, SessionRuntime: first, ExclusiveSession: true,
	})
	if _, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalDangerFullAccess, c.PermissionSnapshot().Revision); err != nil {
		t.Fatal(err)
	}
	c.SetToolApprovalMode(ToolApprovalReadOnly)
	if err := c.rotateExclusiveSession(false); err != nil {
		t.Fatal(err)
	}
	if got := c.ToolApprovalMode(); got != ToolApprovalReadOnly {
		t.Fatalf("new session widened read-only to %q", got)
	}
	if _, err := c.OpenSession(t.Context(), first.Ref()); err != nil {
		t.Fatal(err)
	}
	if got := c.ToolApprovalMode(); got != ToolApprovalReadOnly {
		t.Fatalf("resume widened read-only to stored preset %q", got)
	}
}

func TestServeForkStartsNoBroaderThanInheritedReadOnly(t *testing.T) {
	service, _, c := newForkTargetsHarness(t, "readonly-parent", testutil.Turn{Text: "answer"})
	c.EnableServeSessionPermissionPresets(true)
	if _, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalReadOnly, c.PermissionSnapshot().Revision); err != nil {
		t.Fatal(err)
	}
	if err := c.RunTurn(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	targets, err := c.ForkTargets()
	if err != nil {
		t.Fatal(err)
	}
	target := targets.Targets[0]
	result, err := service.CreateFork(t.Context(), session.ForkRequest{
		Source: targets.Source, TurnID: target.TurnID, BoundarySequence: target.BoundarySequence,
		OperationID: "fork-readonly",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenSession(t.Context(), result.Child); err != nil {
		t.Fatal(err)
	}
	if got := c.ToolApprovalMode(); got != ToolApprovalReadOnly {
		t.Fatalf("fork widened inherited read-only to %q", got)
	}
}

func TestServeRebuildOptInPreservesLiveModeUntilSessionSwitch(t *testing.T) {
	restoreSandbox := SetPresetSandboxForTest(true)
	t.Cleanup(restoreSandbox)
	service, err := session.NewService("serve-test", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Create(t.Context(), session.CreateOptions{SessionID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(t.Context(), session.CreateOptions{SessionID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	c := newOwnedTestController(t, Options{
		Executor: agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard),
		Sink:     event.Discard, SessionService: service, SessionRuntime: first, ExclusiveSession: true,
	})
	if _, _, err := c.SetSessionPermissionPreset(t.Context(), ToolApprovalDangerFullAccess, c.PermissionSnapshot().Revision); err != nil {
		t.Fatal(err)
	}
	c.SetToolApprovalMode(ToolApprovalReadOnly)
	c.EnableServeSessionPermissionPresets(false)
	if got := c.ToolApprovalMode(); got != ToolApprovalReadOnly {
		t.Fatalf("rebuild opt-in widened live mode to %q", got)
	}
	if _, err := c.OpenSession(t.Context(), second.Ref()); err != nil {
		t.Fatal(err)
	}
	if got := c.ToolApprovalMode(); got != ToolApprovalWorkspaceWrite {
		t.Fatalf("new remote session preset = %q", got)
	}
	if _, err := c.OpenSession(t.Context(), first.Ref()); err != nil {
		t.Fatal(err)
	}
	if got := c.ToolApprovalMode(); got != ToolApprovalDangerFullAccess {
		t.Fatalf("stored remote preset was not restored: %q", got)
	}
}
