package control

import (
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/tool"
	"testing"
)

func TestReservedSessionUsesOrdinaryCreationPromptOnce(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := service.Create(t.Context(), session.CreateOptions{SessionID: "reserved"})
	if err != nil {
		t.Fatal(err)
	}
	executor := agent.New(nil, tool.NewRegistry(), agent.NewSession("system fixture"), agent.Options{}, event.Discard)
	controller := newOwnedTestController(t, Options{Executor: executor, Sink: event.Discard, SessionService: service, SessionRuntime: reserved, ExclusiveSession: true})
	if err := controller.InitializeReservedSession(t.Context(), reserved.Ref()); err != nil {
		t.Fatal(err)
	}
	snapshot := reserved.Session().ExecutionSnapshot()
	if err := controller.InitializeReservedSession(t.Context(), reserved.Ref()); err != nil {
		t.Fatal(err)
	}
	if len(reserved.Session().ExecutionSnapshot().Projection.ModelMessages) != len(snapshot.Projection.ModelMessages) {
		t.Fatal("retry reseeded prompt")
	}
	ordinary, err := controller.BindFreshSession(t.Context(), "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Query().Snapshot(t.Context(), ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projection.ModelMessages) != len(other.Projection.ModelMessages) {
		t.Fatal("different initial message count")
	}
	for i, message := range snapshot.Projection.ModelMessages {
		if message.Role != other.Projection.ModelMessages[i].Role || message.Content != other.Projection.ModelMessages[i].Content {
			t.Fatal("reserved creation changed provider-visible initial prompt")
		}
	}
}
