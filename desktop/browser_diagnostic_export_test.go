package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/desktop/internal/browserops"
	"reasonix/internal/browser"
	"reasonix/internal/secrets"
	"reasonix/internal/session"
)

type diagnosticRequestHost func(context.Context, string, any, any) error

func (f diagnosticRequestHost) Request(ctx context.Context, method string, params, result any) error {
	return f(ctx, method, params, result)
}

func TestBrowserDiagnosticWriteCorrelationAndRemoteSessionRotation(t *testing.T) {
	app, exec := newBrowserExecutorForTest(t, &fakeBrowserHost{})
	scope := browserDiagnosticScope(localDesktopHostID, "source")
	exec.diagnosticScope = scope
	exec.host = diagnosticRequestHost(func(ctx context.Context, method string, params, result any) error {
		data, _ := json.Marshal(params)
		var fields map[string]any
		_ = json.Unmarshal(data, &fields)
		if method == "host/browser.grant" && fields["diagnosticScope"] != scope {
			t.Fatal("missing scope")
		}
		if method == "host/browser.tabs.open" && (fields["operationId"] != "open-operation" || fields["requestId"] == nil) {
			t.Fatal("missing operation correlation")
		}
		return nil
	})
	if _, err := exec.Open(t.Context(), browser.OpenRequest{OperationID: "open-operation", URL: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
	if rows := app.browserOps.Diagnostics(scope).Operations; len(rows) != 1 || rows[0].State != browserops.StateExecuted {
		t.Fatalf("missing durable evidence: %+v", rows)
	}

	tab := &remoteTab{id: "remote", ref: RemoteTabRef{HostID: "host"}, session: remoteTabSessionState{path: "/same/path", sessionID: "first"}, routing: remoteTabSessionRouting{currentPath: remoteSessionIDRoutePrefix + "first"}}
	app.remoteTabs = map[string]*remoteTab{tab.id: tab}
	first := app.browserExecutorForRemoteTab(tab, "/same/path").(*hostBrowserExecutor)
	tab.routing.currentPath = remoteSessionIDRoutePrefix + "second"
	tab.session.sessionID = "second"
	second := app.browserExecutorForRemoteTab(tab, "/same/path").(*hostBrowserExecutor)
	if first == second || first.diagnosticScope == second.diagnosticScope || !first.revoked.Load() {
		t.Fatal("same path mixed different canonical sessions")
	}
	if app.browserExecutorForRemoteTab(tab, "/stale/path") != nil {
		t.Fatal("accepted stale browser binding")
	}
}

func TestBrowserDiagnosticProvisionalResumeDoesNotChangeOwnership(t *testing.T) {
	app, _ := newBrowserExecutorForTest(t, &fakeBrowserHost{})
	client := &http.Client{}
	tab := &remoteTab{id: "remote", ref: RemoteTabRef{HostID: "host"}, client: client, gen: 1, state: "ready", session: remoteTabSessionState{path: "/old.jsonl", sessionID: "old"}, routing: remoteTabSessionRouting{currentPath: remoteSessionIDRoutePrefix + "old", running: map[string]bool{}}}
	app.remoteTabs = map[string]*remoteTab{tab.id: tab}
	old := app.browserExecutorForRemoteTab(tab, tab.session.path).(*hostBrowserExecutor)
	route := app.beginRemoteTabProvisionalResume(tab.id, tab, client, 1, remoteSessionIDRoutePrefix+"new")
	if !route.active {
		t.Fatal("fixture did not publish provisional route")
	}
	if exec := app.browserExecutorForRemoteTab(tab, "/old.jsonl"); exec != nil {
		t.Fatal("old request acquired provisional new-session attribution")
	}
	// A resume holds sessionMu while RPCs are pending. Browser resolution must
	// reject promptly under remoteTabMu, without inverting that lock order.
	tab.sessionMu.Lock()
	done := make(chan error, 1)
	go func() { _, err := app.resolveRemoteBrowserSession("host", "/old.jsonl"); done <- err }()
	select {
	case err := <-done:
		tab.sessionMu.Unlock()
		if !errors.Is(err, browser.ErrNoGrant) {
			t.Fatalf("transition returned %v", err)
		}
	case <-time.After(3 * time.Second):
		tab.sessionMu.Unlock()
		<-done
		t.Fatal("browser resolution waited for the transition mutex")
	}
	if old.revoked.Load() {
		t.Fatal("provisional switch revoked the old grant")
	}
	if !app.rollbackRemoteTabProvisionalResume(tab.id, tab, client, 1, route) {
		t.Fatal("rollback failed")
	}
	res, err := app.resolveRemoteBrowserSession("host", "/old.jsonl")
	if err != nil || res.exec != old {
		t.Fatalf("rollback lost original executor: %v", err)
	}
	// Even without an active transition, a mismatched canonical route fails closed.
	tab.routing.currentPath = remoteSessionIDRoutePrefix + "new"
	if app.browserExecutorForRemoteTab(tab, "/old.jsonl") != nil || old.revoked.Load() {
		t.Fatal("inconsistent identity changed ownership")
	}
}

func TestBrowserDiagnosticLegacyRemoteBindingRemainsAvailable(t *testing.T) {
	app, _ := newBrowserExecutorForTest(t, &fakeBrowserHost{})
	tab := &remoteTab{id: "legacy", ref: RemoteTabRef{HostID: "host"}, session: remoteTabSessionState{path: "/legacy.jsonl"}, routing: remoteTabSessionRouting{currentPath: "/legacy.jsonl"}}
	app.remoteTabs = map[string]*remoteTab{tab.id: tab}
	res, err := app.resolveRemoteBrowserSession("host", "/legacy.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if res.exec.(*hostBrowserExecutor).diagnosticScope != "" {
		t.Fatal("guessed a canonical identity for a legacy peer")
	}
}

func TestBrowserDiagnosticCancellationRemainsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	host := diagnosticRequestHost(func(ctx context.Context, _ string, _ any, _ any) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan any, 1)
	go func() { done <- captureBrowserHostDiagnostics(ctx, host, browserDiagnosticScope("host", "source")) }()
	<-entered
	cancel()
	data, _ := json.Marshal(<-done)
	if !bytes.Contains(data, []byte("capture_interrupted")) {
		t.Fatalf("lost cancellation evidence: %s", data)
	}
}

func TestBrowserDiagnosticHostCompatibilityAndIdentity(t *testing.T) {
	scope := browserDiagnosticScope("host", "session")
	for _, tc := range []struct {
		name      string
		reply     any
		err       error
		available bool
	}{
		{"supported", map[string]any{"available": true, "scope": scope, "entries": []any{}}, nil, true},
		{"old", nil, errors.New("method not found"), false},
		{"wrong-session", map[string]any{"available": true, "scope": "other"}, nil, false},
		{"oversized", map[string]any{"available": true, "scope": scope, "entries": strings.Repeat("x", 513<<10)}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &fakeBrowserHost{replies: map[string]any{"host/browser.exportDiagnostics": tc.reply}, errs: map[string]error{"host/browser.exportDiagnostics": tc.err}}
			result := captureBrowserHostDiagnostics(t.Context(), host, scope)
			data, _ := json.Marshal(result)
			var parsed struct {
				Available bool `json:"available"`
			}
			if json.Unmarshal(data, &parsed) != nil || parsed.Available != tc.available {
				t.Fatalf("unexpected result %s", data)
			}
		})
	}
	if scope == browserDiagnosticScope("other-host", "session") || browserDiagnosticScope("host", "") != "" {
		t.Fatal("invalid scope isolation")
	}
}

func TestBrowserDiagnosticColdExportIncludesOnlyFixedSource(t *testing.T) {
	app, ref := activityBaselineFixture(t, "browser-diagnostic")
	ledger, err := app.browserLedger()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []browserops.Operation{
		{ID: "source-operation", DiagnosticScope: browserDiagnosticScope(localDesktopHostID, ref.SessionID), Action: "click"},
		{ID: "foreign-operation", DiagnosticScope: browserDiagnosticScope(localDesktopHostID, "other"), Action: "click"},
	} {
		if err = ledger.Reserve(op); err != nil {
			t.Fatal(err)
		}
	}
	if err = app.desktopSessionService("").Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	app.setNativeHost(&recordingNativeHost{dialogPath: path, onCall: func(name string) {
		if strings.HasPrefix(name, "SaveFileDialog:") {
			app.activeTabID = "other"
		}
	}})
	handle, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &ref}, "", "diagnostic", "Browser", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.FinishSessionExport(handle.ExportID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	section := result["browserDiagnostics"]
	var status struct {
		Host struct {
			Available bool `json:"available"`
		} `json:"host"`
	}
	if err = json.Unmarshal(section, &status); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(section, []byte("source-operation")) || bytes.Contains(section, []byte("foreign-operation")) || status.Host.Available {
		t.Fatalf("bad evidence %s", section)
	}
	if op, _ := ledger.Lookup("source-operation"); op.State != browserops.StateReserved {
		t.Fatal("export settled write")
	}
}

func TestBrowserDiagnosticRemoteMergePreservesOldProtocolAndFixedScope(t *testing.T) {
	isolateDesktopUserDirs(t)
	var seenBody []byte
	app, tab := remoteRuntimeTestApp(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/session-export/snapshot":
			return remoteRuntimeTestResponse(req, 200, remoteRuntimeTestJSON(t, session.ExportSnapshot{Ref: session.SessionRef{HostID: "fixture-host", SessionID: "source"}, StorageGeneration: "g"})), nil
		case "/session-export/validate":
			return remoteRuntimeTestResponse(req, 204, ""), nil
		case "/session-export/diagnostic":
			seenBody, _ = io.ReadAll(req.Body)
			return remoteRuntimeTestResponse(req, 200, `{"metadata":{"oldRemote":true},"commits":[{"source":"REMOTE-EVIDENCE"}]}`), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})})
	ledger, err := app.browserLedger()
	if err != nil {
		t.Fatal(err)
	}
	tab.capabilities["session-export-v1"] = true
	tab.routing.currentPath = remoteSessionIDRoutePrefix + "source"
	tab.session.path = "/source/session"
	if err = ledger.Reserve(browserops.Operation{ID: "remote-browser-op", DiagnosticScope: browserDiagnosticScope(tab.ref.HostID, "source")}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "remote.json")
	app.setNativeHost(&recordingNativeHost{dialogPath: path, onCall: func(name string) {
		if strings.HasPrefix(name, "SaveFileDialog:") {
			tab.session.path = "/other/session"
			tab.routing.currentPath = remoteSessionIDRoutePrefix + "other"
		}
	}})
	handle, err := app.BeginSessionExportForTarget(SessionSelector{}, tab.id, "diagnostic", "Remote", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.FinishSessionExport(handle.ExportID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !json.Valid(data) || !bytes.Contains(data, []byte("REMOTE-EVIDENCE")) || !bytes.Contains(data, []byte("remote-browser-op")) {
		t.Fatalf("bad remote merge %s", data)
	}
	if bytes.Contains(seenBody, []byte("browserDiagnostics")) {
		t.Fatal("new evidence sent through old remote observation protocol")
	}
}

func TestBrowserDiagnosticAppendHandlesEmptyAndLargeDocuments(t *testing.T) {
	for _, raw := range []string{"{}\n", `{"commits":["` + strings.Repeat("x", 2<<20) + `"]}` + "\n"} {
		path := filepath.Join(t.TempDir(), "export.json")
		err := writeGoalDiagnosticsFile(path, func(dst io.Writer) error {
			if _, err := io.WriteString(dst, raw); err != nil {
				return err
			}
			return appendBrowserDiagnosticSection(dst, []byte(`{"available":true}`))
		})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		if !json.Valid(data) {
			t.Fatal("invalid appended JSON")
		}
	}
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	err := writeGoalDiagnosticsFile(path, func(dst io.Writer) error {
		_, _ = io.WriteString(dst, `{"commits":[`)
		return appendBrowserDiagnosticSection(dst, []byte(`{}`))
	})
	data, _ := os.ReadFile(path)
	if err == nil || string(data) != "original" {
		t.Fatal("partial remote response replaced destination")
	}
}

// Native qualification can feed the real Electron report through the same Go
// validation, redaction and file merge used by remote session exports. Normal
// unit runs use a small host-protocol fixture and need no Node/Electron runtime.
func TestBrowserDiagnosticHostEvidenceFinalJSON(t *testing.T) {
	scope := strings.Repeat("a", 64)
	report := json.RawMessage(`{"available":true,"scope":"` + scope + `","entries":[{"kind":"page","message":"https://example.test/path http://example.test/path"}]}`)
	if fixture := os.Getenv("REASONIX_BROWSER_DIAGNOSTIC_FIXTURE"); fixture != "" {
		data, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		report = data
	}
	host := &fakeBrowserHost{replies: map[string]any{"host/browser.exportDiagnostics": report}}
	evidence := captureBrowserHostDiagnostics(t.Context(), host, scope)
	encoded, err := json.Marshal(map[string]any{"host": evidence})
	if err != nil {
		t.Fatal(err)
	}
	encoded = []byte(secrets.Redact(string(encoded)))
	path := filepath.Join(t.TempDir(), "final-diagnostics.json")
	if err = writeGoalDiagnosticsFile(path, func(dst io.Writer) error {
		if _, err := io.WriteString(dst, `{"commits":[]}`); err != nil {
			return err
		}
		return appendBrowserDiagnosticSection(dst, encoded)
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		Browser struct {
			Host struct {
				Available bool              `json:"available"`
				Entries   []json.RawMessage `json:"entries"`
			} `json:"host"`
		} `json:"browserDiagnostics"`
	}
	if err = json.Unmarshal(data, &exported); err != nil {
		t.Fatal(err)
	}
	if !exported.Browser.Host.Available || len(exported.Browser.Host.Entries) == 0 {
		t.Fatal("lost native diagnostic evidence")
	}
	for _, secret := range []string{"NATIVE-SECRET", "REVIEW-OAUTH-CODE", "REVIEW-PASSWORD", "REVIEW-FRAGMENT", "REVIEW-SIGNATURE"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("secret survived final JSON: %s", secret)
		}
	}
	if !bytes.Contains(data, []byte("https://example.test/path")) || !bytes.Contains(data, []byte("http://example.test/path")) {
		t.Fatal("sanitized URL evidence was lost")
	}
}
