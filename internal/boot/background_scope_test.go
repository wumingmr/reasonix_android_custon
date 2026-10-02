package boot

import (
	"context"
	"io"
	"reasonix/internal/control"
	"testing"
)

func TestRebuildBackgroundCandidateFailurePreservesOwner(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	writeRuntimeFixture(t, root)
	old := buildRuntimeFixture(t).Controller
	scope := old.BackgroundScope()
	initialGeneration := old.RuntimeOwner().Gate.Published()
	entered := make(chan struct{})
	job := scope.Manager.StartSessionProcess("", "bash", "gateway", func(ctx context.Context, _ io.Writer) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	})
	<-entered
	if _, err := Rebuild(t.Context(), old, Options{Model: "missing/provider"}); err == nil {
		t.Fatal("expected invalid model failure")
	}
	if len(scope.Manager.Running()) != 1 {
		t.Fatal("failed build killed gateway")
	}
	candidate, err := Rebuild(t.Context(), old, Options{})
	if err != nil {
		t.Fatal(err)
	}
	candidate.Controller.ReleaseResources()
	if old.RuntimeOwner().Gate.Published() != initialGeneration {
		t.Fatal("discarded candidate retired outgoing extension generation")
	}
	if len(scope.Manager.Running()) != 1 || scope.Manager.ReplacementInProgress() {
		t.Fatal("discard did not restore outgoing ownership")
	}
	next, err := Rebuild(t.Context(), old, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := control.ActivateControllerReplacement(old, next.Controller); err != nil {
		t.Fatal(err)
	}
	if old.RuntimeOwner().Gate.Published() != next.Controller.RuntimeGeneration() {
		t.Fatal("activation did not publish extension generation")
	}
	old.ReleaseResources()
	if len(scope.Manager.Running()) != 1 {
		t.Fatal("old controller close killed gateway")
	}
	next.Controller.Close()
	scope.Manager.Wait(t.Context(), []string{job.ID}, 10)
	if len(scope.Manager.Running()) != 0 {
		t.Fatal("session shutdown left gateway running")
	}
}
