package boot

import (
	"encoding/json"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/extension/protocol"
)

func TestModelCandidateDiscardPreservesLiveExtensionManager(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	writeRuntimeFixture(t, root)
	installBootFakePlugin(t, config.ReasonixHomeDir(), "preserved", map[string]any{})
	old, err := BuildRuntime(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Controller.Close)
	client := old.Extensions.Client("preserved")
	options := Options{RuntimeReload: RuntimeReload{Extensions: old.Extensions, Graph: old.Plan.Graph}}
	options.Model = "missing/model"
	if _, err := Rebuild(t.Context(), old.Controller, options); err == nil {
		t.Fatal("expected model resolution failure")
	}
	options.Model = ""
	candidate, err := Rebuild(t.Context(), old.Controller, options)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Extensions.Client("preserved") == client {
		t.Fatal("candidate stole outgoing sidecar before publication")
	}
	candidate.Controller.ReleaseResources()
	if old.Extensions.Client("preserved") != client || client.Exited() {
		t.Fatal("discard retired live extension manager")
	}
	result, err := client.Intercept(t.Context(), protocol.EventSessionStart, json.RawMessage(`{}`), 5*time.Second)
	if err != nil || result.Decision != protocol.DecisionContinue {
		t.Fatalf("outgoing extension lost after discard: %v, %v", result, err)
	}
}
