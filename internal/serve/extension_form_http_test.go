package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

type extensionFormController struct {
	control.SessionAPI
	pluginID, surfaceID string
	values              map[string]any
	generation          uint64
	instanceID          string
	exactCalls          int
	sessionID           string
}

func (c *extensionFormController) RuntimeStateSnapshot() event.RuntimeStateSnapshot {
	return event.RuntimeStateSnapshot{SchemaVersion: 1, SessionID: c.sessionID}
}

func (c *extensionFormController) SubmitExtensionFormExact(_ context.Context, pluginID, surfaceID string, generation uint64, instanceID string, values map[string]any) error {
	c.exactCalls++
	c.pluginID, c.surfaceID, c.generation, c.instanceID, c.values = pluginID, surfaceID, generation, instanceID, values
	return nil
}

func (c *extensionFormController) SubmitExtensionForm(_ context.Context, pluginID, surfaceID string, values map[string]any) error {
	c.pluginID, c.surfaceID, c.values = pluginID, surfaceID, values
	return nil
}

func TestServeExactExtensionFormChecksSessionBeforeController(t *testing.T) {
	bc := NewBroadcaster()
	ctrl := &extensionFormController{SessionAPI: control.New(control.Options{Sink: bc}), sessionID: "session-a"}
	srv := httptest.NewServer(operatorHandler(New(ctrl, bc, config.ServeConfig{})))
	defer srv.Close()
	post := func(sessionID string) int {
		resp, err := http.Post(srv.URL+"/extension-form", "application/json", strings.NewReader(
			`{"sessionId":"`+sessionID+`","pluginId":"remote-plugin","surfaceId":"setup","generation":7,"formInstanceId":"form-2","values":{"region":"sg"}}`,
		))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("session-old"); got != http.StatusConflict || ctrl.exactCalls != 0 {
		t.Fatalf("stale exact form status/calls = %d/%d", got, ctrl.exactCalls)
	}
	if got := post("session-a"); got != http.StatusNoContent || ctrl.exactCalls != 1 || ctrl.generation != 7 || ctrl.instanceID != "form-2" {
		t.Fatalf("current exact form status/call = %d/%+v", got, ctrl)
	}
}

func TestServeExtensionFormSubmissionRoutesToController(t *testing.T) {
	bc := NewBroadcaster()
	ctrl := &extensionFormController{SessionAPI: control.New(control.Options{Sink: bc})}
	srv := httptest.NewServer(operatorHandler(New(ctrl, bc, config.ServeConfig{})))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/extension-form", "application/json", strings.NewReader(
		`{"pluginId":"remote-plugin","surfaceId":"setup","values":{"region":"us-west"}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if ctrl.pluginID != "remote-plugin" || ctrl.surfaceID != "setup" || ctrl.values["region"] != "us-west" {
		t.Fatalf("submission = %q/%q/%v", ctrl.pluginID, ctrl.surfaceID, ctrl.values)
	}
}
