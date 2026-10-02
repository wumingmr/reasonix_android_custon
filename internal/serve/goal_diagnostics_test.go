package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestGoalDiagnosticsHTTPExportsAuthoritativeSession(t *testing.T) {
	service, err := session.NewService("serve", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown session service: %v", err)
		}
	})
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "diagnostic-http"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	t.Cleanup(ctrl.Close)
	server := &Server{ctrl: ctrl}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/goal-diagnostics", nil)
	server.goalDiagnostics(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, want := range []string{session.Codec, "goal-lifecycle-v2", "activationChanges"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("diagnostic response missing %q: %s", want, recorder.Body.String())
		}
	}
}
