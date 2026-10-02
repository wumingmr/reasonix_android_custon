package control

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func compactionPersistenceHistory(tools bool) *agent.Session {
	s := agent.NewSession("system")
	if tools {
		s.Add(provider.Message{Role: provider.RoleUser, Content: "inspect the files"})
		for i := range 3 {
			id := fmt.Sprintf("call-%d", i)
			s.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: id, Name: "read_file", Arguments: `{}`}}})
			s.Add(provider.Message{Role: provider.RoleTool, ToolCallID: id, Name: "read_file", Content: strings.Repeat("x", 16_000)})
		}
	} else {
		for i := range 24 {
			s.Add(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("task %d", i)})
			s.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("x", 2_000)})
		}
	}
	s.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "ready"})
	return s
}

func TestContextMaintenanceProjectionSurvivesSessionSwitch(t *testing.T) {
	for _, mode := range []string{"summary", "prune", "failure", "auto-summary", "auto-prune", "auto-failure"} {
		t.Run(mode, func(t *testing.T) {
			service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v5")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
			runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "maintenance"})
			if err != nil {
				t.Fatal(err)
			}
			providerMock := testutil.NewMock("maintenance", testutil.Turn{Text: "durable summary"}, testutil.Turn{Text: "final answer"}, testutil.Turn{Text: "final answer"})
			if strings.Contains(mode, "failure") {
				providerMock = testutil.NewMock("maintenance", testutil.Turn{StreamError: errors.New("summary unavailable")}, testutil.Turn{Text: "final answer"})
			}
			exec := agent.New(providerMock, tool.NewRegistry(), compactionPersistenceHistory(strings.Contains(mode, "prune")), agent.Options{ContextWindow: 10_000, CompactRatio: .8}, event.Discard)
			controller := newOwnedTestController(t, Options{Runner: exec, Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})

			canonicalBefore := runtime.Session().ExecutionSnapshot().Projection.Messages
			if strings.HasPrefix(mode, "auto-") {
				err = controller.RunTurn(t.Context(), "continue the task")
			} else if mode == "prune" {
				err = exec.PrepareContext(t.Context())
			} else {
				err = controller.Compact(t.Context(), "")
			}
			if strings.Contains(mode, "failure") {
				if !errors.Is(err, agent.ErrCompactionRequired) {
					t.Fatalf("failed summary = %v, want blocked turn", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}

			wantModel := provider.ModelMessages(exec.ModelHistorySnapshot())
			snapshot := runtime.Session().ExecutionSnapshot()
			if !reflect.DeepEqual(snapshot.Projection.ModelMessages, wantModel) {
				t.Fatal("durable model projection differs from the installed projection")
			}
			if !strings.HasPrefix(mode, "auto-") && !reflect.DeepEqual(snapshot.Projection.Messages, canonicalBefore) {
				t.Fatal("context maintenance rewrote canonical history")
			}

			other, err := service.Create(t.Context(), session.CreateOptions{SessionID: "other"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := controller.OpenSession(t.Context(), other.Ref()); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.OpenSession(t.Context(), runtime.Ref()); err != nil {
				t.Fatal(err)
			}
			if got := provider.ModelMessages(exec.ModelHistorySnapshot()); !reflect.DeepEqual(got, wantModel) {
				t.Fatal("model projection changed after switching away and reopening")
			}
		})
	}
}
