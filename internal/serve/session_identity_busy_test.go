package serve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
)

// newIdentityBusyServe wires an exclusive-session serve whose replacement
// controllers inherit the shared session service, mirroring what a production
// identity-capable host stores in its boot options.
func newIdentityBusyServe(t *testing.T) (*httptest.Server, *Server, *control.Controller, *session.Service, session.SessionRef) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sessions-v4")
	service, err := session.NewService("serve-test", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown session service: %v", err)
		}
	})
	exec := agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard)
	opts := control.Options{
		Runner: blockingRunner{}, Executor: exec, SessionDir: t.TempDir(),
		SessionService: service, ExclusiveSession: true,
	}
	ctrl := control.New(opts)
	ref, err := ctrl.BindFreshSession(t.Context(), "current")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctrl.Close)
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	bc := NewBroadcaster()
	srv := New(ctrl, bc, config.ServeConfig{})
	srv.buildControllerWithOptions = func(_ context.Context, _ string, bootOpts boot.Options) (*control.Controller, error) {
		replacement := control.New(control.Options{
			Runner: blockingRunner{}, Executor: agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard),
			SessionDir: bootOpts.SessionDir, WorkspaceRoot: bootOpts.WorkspaceRoot,
			SessionService: service, ExclusiveSession: true,
			Sink: bootOpts.Sink, Label: "test",
		})
		return replacement, nil
	}
	tag := newSessionTagSink(bc)
	if id, bound := ctrl.SessionRef(); bound {
		tag.SetIdentity("", id.SessionID)
	}
	srv.RegisterSessionTag(ctrl, tag)
	httpSrv := httptest.NewServer(operatorHandler(srv))
	t.Cleanup(httpSrv.Close)
	t.Cleanup(srv.CloseBackground)
	return httpSrv, srv, ctrl, service, ref
}

func postResumeIdentity(t *testing.T, httpSrv *httptest.Server, sessionID string) *http.Response {
	t.Helper()
	resp, err := http.Post(httpSrv.URL+"/resume", "application/json",
		strings.NewReader(`{"hostId":"serve-test","sessionId":"`+url.QueryEscape(sessionID)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The identity-route counterpart of TestBusyResumeDetachesAndReattachesRunningController:
// switching away from a running exclusive session must background it (turn
// keeps running, transcript stays readable) instead of refusing with 409, and
// switching back must re-attach the same live controller.
func TestBusyIdentityResumeDetachesAndReattachesRunningController(t *testing.T) {
	httpSrv, srv, ctrlA, service, refA := newIdentityBusyServe(t)
	target, err := service.Create(t.Context(), session.CreateOptions{SessionID: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), target.Ref()); err != nil {
		t.Fatal(err)
	}

	ctrlA.Submit("keep running")
	waitRunning(t, ctrlA)

	resp := postResumeIdentity(t, httpSrv, "target")
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("busy resume = %d: %s", resp.StatusCode, body)
	}
	foreground, ok := srv.ctl().(*control.Controller).SessionRef()
	if !ok || foreground.SessionID != "target" {
		t.Fatalf("foreground identity = %+v bound=%v, want target", foreground, ok)
	}
	if got := srv.ctl().SessionPath(); got != "" {
		t.Fatalf("foreground path = %q, want empty for identity sessions", got)
	}
	if !ctrlA.Running() {
		t.Fatal("switched-away session stopped instead of running in background")
	}
	// The backgrounded session stays readable through its identity route.
	if resolved := srv.resolveReadControllerLocked(remoteSessionIDQueryPrefix + refA.SessionID); resolved != control.SessionAPI(ctrlA) {
		t.Fatal("backgrounded running session is not readable through its identity route")
	}

	back := postResumeIdentity(t, httpSrv, refA.SessionID)
	if back.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(back.Body)
		t.Fatalf("reattach resume = %d: %s", back.StatusCode, body)
	}
	if srv.ctl() != control.SessionAPI(ctrlA) {
		t.Fatal("reattach did not restore the original controller")
	}
	if !ctrlA.Running() {
		t.Fatal("running turn was lost during reattach")
	}
	ctrlA.Cancel()
	waitNotRunning(t, ctrlA)
}

// A host whose replacement builders cannot open identity sessions keeps the
// historical refusal instead of dropping the running controller.
func TestBusyIdentityResumeWithoutServiceRefusesLikeBefore(t *testing.T) {
	httpSrv, srv, ctrlA, service, _ := newIdentityBusyServe(t)
	target, err := service.Create(t.Context(), session.CreateOptions{SessionID: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), target.Ref()); err != nil {
		t.Fatal(err)
	}
	// Strip the service from replacements: a legacy host that never wired the
	// session service into its boot options.
	srv.buildControllerWithOptions = func(_ context.Context, _ string, bootOpts boot.Options) (*control.Controller, error) {
		return control.New(control.Options{
			Runner: blockingRunner{}, SessionDir: bootOpts.SessionDir,
			Sink: bootOpts.Sink, Label: "test",
		}), nil
	}

	ctrlA.Submit("keep running")
	waitRunning(t, ctrlA)

	resp := postResumeIdentity(t, httpSrv, "target")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("refusal resume = %d, want 409", resp.StatusCode)
	}
	if srv.ctl() != control.SessionAPI(ctrlA) {
		t.Fatal("refused switch replaced the foreground controller")
	}
	if !ctrlA.Running() {
		t.Fatal("refused switch stopped the running turn")
	}
	ctrlA.Cancel()
	waitNotRunning(t, ctrlA)
}

// Re-selecting the running foreground session is a no-op, not a swap.
func TestBusyIdentityResumeOfCurrentSessionIsNoop(t *testing.T) {
	httpSrv, srv, ctrlA, _, refA := newIdentityBusyServe(t)
	ctrlA.Submit("keep running")
	waitRunning(t, ctrlA)

	resp := postResumeIdentity(t, httpSrv, refA.SessionID)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("same-session resume = %d: %s", resp.StatusCode, body)
	}
	if srv.ctl() != control.SessionAPI(ctrlA) {
		t.Fatal("same-session resume replaced the foreground controller")
	}
	if !ctrlA.Running() {
		t.Fatal("same-session resume stopped the running turn")
	}
	ctrlA.Cancel()
	waitNotRunning(t, ctrlA)
}
