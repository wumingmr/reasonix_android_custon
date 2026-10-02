package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestExportRemoteGoalDiagnosticsUsesSessionFence(t *testing.T) {
	var expectedPath string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		expectedPath = req.Header.Get(expectedSessionPathHeader)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"schemaVersion":1}`)), Request: req}, nil
	})}
	app, tab := remoteRuntimeTestApp(client)
	app.ctx = context.Background()
	tab.capabilities[serveCapabilityGoalLifecycleV2] = true
	payload, err := app.exportRemoteGoalDiagnostics(tab.id)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"schemaVersion":1}` || expectedPath != runtimeRemoteTestPath {
		t.Fatalf("payload/path = %s / %q", payload, expectedPath)
	}
}
