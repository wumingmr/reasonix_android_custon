package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reasonix/internal/control"
	"reasonix/internal/proc"
	"reasonix/internal/provider"
	"strings"
	"testing"
	"time"
)

type modelObservedOutput struct {
	writer io.Writer
	wrote  chan<- struct{}
}

func (w modelObservedOutput) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return n, err
}

func TestModelGatewayProcess(t *testing.T) {
	if os.Getenv("REASONIX_TEST_GATEWAY_PROCESS") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}
	os.Exit(0)
}

func modelApplicationFixture(t *testing.T) (*App, *WorkspaceTab, ProviderView, <-chan struct{}) {
	t.Helper()
	isolateDesktopUserDirs(t)
	called := make(chan struct{}, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		called <- struct{}{}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	a := NewApp()
	t.Cleanup(a.closeSessionServices)
	view := ProviderView{Name: "gateway-model", Kind: "openai", BaseURL: server.URL, Models: []string{"m"}, NoProxy: true}
	if _, err := a.SaveProviderWithKey(view, "fixture-key"); err != nil {
		t.Fatal(err)
	}
	a.ctx = t.Context()
	a.readyHook = func() {}
	tab := modelSettingsBootTab(t, a, "gateway", t.TempDir(), "gateway-model/m")
	tab.toolApprovalMode = tab.Ctrl.ToolApprovalMode()
	a.activeTabID = tab.ID
	return a, tab, view, called
}

func TestModelSettingsGatewaySurvivesReplacementAndFailure(t *testing.T) {
	a, tab, view, called := modelApplicationFixture(t)
	old := tab.Ctrl.(*control.Controller)
	scope := old.BackgroundScope()
	ref, _ := old.SessionRef()
	type process struct {
		pid   int
		input io.WriteCloser
	}
	ready := make(chan process, 1)
	exited := make(chan struct{})
	wrote := make(chan struct{}, 16)
	job, err := scope.Manager.TryStartSessionProcess(ref.SessionID, "bash", "gateway", func(ctx context.Context, out io.Writer) (string, error) {
		defer close(exited)
		cmd := proc.CommandContext(ctx, os.Args[0], "-test.run=^TestModelGatewayProcess$")
		cmd.Env = append(os.Environ(), "REASONIX_TEST_GATEWAY_PROCESS=1")
		cmd.Stdout = modelObservedOutput{writer: out, wrote: wrote}
		cmd.Stderr = out
		input, err := cmd.StdinPipe()
		if err != nil {
			return "", err
		}
		if err = cmd.Start(); err != nil {
			return "", err
		}
		ready <- process{cmd.Process.Pid, input}
		return "", cmd.Wait()
	})
	if err != nil {
		t.Fatal(err)
	}
	var p process
	select {
	case p = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("process failed to start")
	}
	defer p.input.Close()
	for i := range 2 {
		// Query changes leave the fixture endpoint valid while changing the resolver.
		view.BaseURL = strings.Split(view.BaseURL, "?")[0] + fmt.Sprintf("?generation=%d", i)
		if _, err := a.SaveProviderWithKey(view, "fixture-key"); err != nil {
			t.Fatal(err)
		}
		before := tab.Ctrl
		admission, current, err := a.beginTabTurn(tab.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		admission.abort()
		next := current.(*control.Controller)
		nextRef, _ := next.SessionRef()
		if current == before || next.BackgroundScope() != scope || next.RuntimeStatus().BackgroundJobs != 1 {
			output, status, _ := scope.Manager.OutputForSession(ref.SessionID, job.ID)
			t.Logf("gateway status=%s output=%s", status, output)
			t.Fatalf("replacement lost live gateway: changed=%v sameScope=%v status=%+v all=%+v ref=%+v next=%+v", current != before, next.BackgroundScope() == scope, next.RuntimeStatus(), scope.Manager.Running(), ref, nextRef)
		}
		select {
		case <-exited:
			t.Fatal("gateway exited during replacement")
		default:
		}
		if _, err := fmt.Fprintf(p.input, "pid=%d generation=%d\n", p.pid, i); err != nil {
			t.Fatal(err)
		}
		marker := fmt.Sprintf("pid=%d generation=%d", p.pid, i)
		deadline := time.After(3 * time.Second)
		for {
			output, _, _ := scope.Manager.OutputForSession(ref.SessionID, job.ID)
			if strings.Contains(output, marker) {
				break
			}
			select {
			case <-wrote:
			case <-deadline:
				t.Fatal("gateway output was lost after replacement")
			}
		}
		if err := next.RunTurn(t.Context(), "answer briefly"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("new model endpoint was not called")
		}
		users := 0
		for _, message := range next.History() {
			if message.Role == provider.RoleUser && message.Content == "answer briefly" {
				users++
			}
		}
		if users != i+1 {
			t.Fatalf("history lost across replacement: users=%d", users)
		}
	}
	if !tab.Ctrl.(*control.Controller).CancelJob(job.ID) {
		t.Fatal("replacement cannot stop original job")
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("gateway did not exit")
	}
	scope.Manager.WaitForSession(t.Context(), ref.SessionID, []string{job.ID}, 10)
	if got := scope.Manager.RunningForSession(ref.SessionID); len(got) != 0 {
		t.Fatalf("running jobs after exit: %+v", got)
	}
}

func TestModelSettingsRuntimeTaskChoiceIsSingleUseAndRevocationSafe(t *testing.T) {
	a, tab, view, _ := modelApplicationFixture(t)
	c := tab.Ctrl.(*control.Controller)
	ref, _ := c.SessionRef()
	started := make(chan struct{})
	job := c.BackgroundScope().Manager.StartForSession(ref.SessionID, "task", "dependent", func(ctx context.Context, _ io.Writer) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	<-started
	view.BaseURL = strings.Replace(view.BaseURL, "127.0.0.1", "localhost", 1)
	if _, err := a.SaveProviderWithKey(view, "fixture-key"); err != nil {
		t.Fatal(err)
	}
	if admission, _, err := a.beginTabTurn(tab.ID, false); err == nil {
		admission.abort()
		t.Fatal("dependent task allowed rebuild")
	}
	d := modelApplicationDetails(c)
	if !d.CanUseApplied || len(d.BlockingJobs) != 1 {
		t.Fatalf("recovery details: %+v", d)
	}
	choice := control.ModelApplicationChoice{Mode: "applied_once", ExpectedAppliedRevision: d.AppliedRevision, ExpectedDesiredRevision: d.DesiredRevision, ExpectedRuntimeIdentity: d.RuntimeIdentity}
	admission, current, err := a.beginRuntimeTurnWithModelChoice(tab.ID, false, false, nil, &choice)
	if err != nil {
		t.Fatal(err)
	}
	admission.abort()
	if current != c {
		t.Fatal("choice changed controller")
	}
	if admission, _, err := a.beginTabTurn(tab.ID, false); err == nil {
		admission.abort()
		t.Fatal("choice leaked to next submit")
	}
	if _, err := a.SaveProviderWithKey(view, "rotated-key"); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateModelApplicationChoice(choice); err == nil {
		t.Fatal("stale choice accepted")
	}
	if modelApplicationDetails(c).CanUseApplied {
		t.Fatal("rotated credentials allowed old snapshot")
	}
	c.CancelJob(job.ID)
	c.BackgroundScope().Manager.WaitForSession(t.Context(), ref.SessionID, []string{job.ID}, 10)
	a.deferredRebuildTick(false)
	if tab.Ctrl == c {
		t.Fatal("task completion did not permit deferred configuration application")
	}
}
