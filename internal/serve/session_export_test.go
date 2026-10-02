package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestSessionExportHTTPFixedCompleteSnapshot(t *testing.T) {
	service, err := session.NewService("serve", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "export"})
	if err != nil {
		t.Fatal(err)
	}
	appendMessage := func(id string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: id, Origin: provider.MessageOrigin("user")}})
		if _, err := runtime.Session().AppendBatch(t.Context(), id, []session.Event{{Kind: "message/complete", Payload: body}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 110 {
		appendMessage(fmt.Sprintf("question-%03d", i))
	}
	bc := NewBroadcaster()
	ctrl := control.New(control.Options{SessionService: service, SessionRuntime: runtime, ExclusiveSession: true, Sink: bc})
	defer ctrl.Close()
	server := httptest.NewServer(operatorHandler(New(ctrl, bc, config.ServeConfig{})))
	defer server.Close()
	response, err := http.Get(server.URL + "/session-export/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot session.ExportSnapshot
	err = json.NewDecoder(response.Body).Decode(&snapshot)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot: %d %v", response.StatusCode, err)
	}
	appendMessage("AFTER-SNAPSHOT")
	request, _ := json.Marshal(map[string]any{"snapshot": snapshot, "format": "json"})
	response, err = http.Post(server.URL+"/session-export/document", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Reasonix-Export-Records") != "110" || !json.Valid(body) {
		t.Fatalf("response: %d %s", response.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("question-000")) || !bytes.Contains(body, []byte("question-109")) || bytes.Contains(body, []byte("AFTER-SNAPSHOT")) {
		t.Fatal("export was truncated or changed its snapshot")
	}
	snapshot.Ref.SessionID = "other"
	request, _ = json.Marshal(map[string]any{"snapshot": snapshot, "format": "json"})
	response, err = http.Post(server.URL+"/session-export/document", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("accepted mismatched identity: %d", response.StatusCode)
	}
	malicious, _ := json.Marshal(map[string]any{"snapshot": snapshot, "format": "../../manifest.json"})
	response, err = http.Post(server.URL+"/session-export/document", "application/json", bytes.NewReader(malicious))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("accepted path-like export format: %d", response.StatusCode)
	}
	response, err = http.Post(server.URL+"/session-export/diagnostic", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !json.Valid(body) {
		t.Fatalf("diagnostic: %d %v", response.StatusCode, err)
	}
}

func appendSessionExportTestMessage(t *testing.T, runtime *session.Runtime, id string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: id, Origin: provider.MessageOrigin("user")}})
	if _, err := runtime.Session().AppendBatch(t.Context(), id, []session.Event{{Kind: "message/complete", Payload: body}}); err != nil {
		t.Fatal(err)
	}
}

func captureSessionExportTestSnapshot(t *testing.T, serverURL string, ref session.SessionRef, diagnostic bool) session.ExportSnapshot {
	t.Helper()
	endpoint := serverURL + "/session-export/snapshot?sessionId=" + ref.SessionID
	if diagnostic {
		endpoint += "&diagnostic=1"
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(expectedSessionIDHeader, ref.SessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("snapshot status = %d: %s", resp.StatusCode, body)
	}
	var snapshot session.ExportSnapshot
	if err = json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func postFixedSessionExport(t *testing.T, serverURL, path, queryID, headerID string, body any) (*http.Response, []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, serverURL+path+"?sessionId="+url.QueryEscape(queryID), bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if headerID != "" {
		req.Header.Set(expectedSessionIDHeader, headerID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func TestSessionExportRemainsPinnedAfterForegroundSwitch(t *testing.T) {
	srv, ctrl, service, source := newExclusiveSessionServe(t)
	runtime, ok := service.Runtime(source)
	if !ok {
		t.Fatal("source runtime is unavailable")
	}
	appendSessionExportTestMessage(t, runtime, "SOURCE-A")
	ts := httptest.NewServer(operatorHandler(srv))
	defer ts.Close()
	snapshot := captureSessionExportTestSnapshot(t, ts.URL, source, true)

	target, err := ctrl.BindFreshSession(t.Context(), "target-b")
	if err != nil {
		t.Fatal(err)
	}
	targetRuntime, ok := service.Runtime(target)
	if !ok {
		t.Fatal("target runtime is unavailable")
	}
	appendSessionExportTestMessage(t, targetRuntime, "TARGET-B")

	resp, body := postFixedSessionExport(t, ts.URL, "/session-export/document", source.SessionID, source.SessionID, map[string]any{"snapshot": snapshot, "format": "json"})
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("SOURCE-A")) || bytes.Contains(body, []byte("TARGET-B")) {
		t.Fatalf("fixed document status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = postFixedSessionExport(t, ts.URL, "/session-export/validate", source.SessionID, source.SessionID, snapshot)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("fixed validation status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = postFixedSessionExport(t, ts.URL, "/session-export/diagnostic", source.SessionID, source.SessionID, map[string]any{"exportSnapshot": snapshot})
	if resp.StatusCode != http.StatusOK || !json.Valid(body) || !bytes.Contains(body, []byte("SOURCE-A")) || bytes.Contains(body, []byte("TARGET-B")) || !bytes.Contains(body, []byte("cold session")) {
		t.Fatalf("fixed diagnostic status=%d body=%s", resp.StatusCode, body)
	}

	resp, body = postFixedSessionExport(t, ts.URL, "/session-export/document", target.SessionID, source.SessionID, map[string]any{"snapshot": snapshot, "format": "json"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting query/header/body status=%d body=%s", resp.StatusCode, body)
	}
	if err = service.Close(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err = service.Delete(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	resp, body = postFixedSessionExport(t, ts.URL, "/session-export/document", source.SessionID, source.SessionID, map[string]any{"snapshot": snapshot, "format": "json"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deleted source export status=%d body=%s", resp.StatusCode, body)
	}
}

func TestSessionExportRejectsPathLikeTargetIdentities(t *testing.T) {
	srv, _, _, source := newExclusiveSessionServe(t)
	ts := httptest.NewServer(operatorHandler(srv))
	defer ts.Close()
	snapshot := captureSessionExportTestSnapshot(t, ts.URL, source, false)

	for _, target := range []string{
		"../" + source.SessionID,
		source.SessionID + "/child",
		`C:\\outside\\` + source.SessionID,
		".query-cache",
	} {
		t.Run(target, func(t *testing.T) {
			resp, body := postFixedSessionExport(t, ts.URL, "/session-export/document", target, "", map[string]any{
				"snapshot": snapshot,
				"format":   "json",
			})
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("path-like query target status=%d body=%s", resp.StatusCode, body)
			}
		})
	}

	invalidSnapshot := snapshot
	invalidSnapshot.Ref.SessionID = "../" + source.SessionID
	resp, body := postFixedSessionExport(t, ts.URL, "/session-export/validate", "", "", invalidSnapshot)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("path-like snapshot target status=%d body=%s", resp.StatusCode, body)
	}
}

func TestSessionExportRemainsPinnedAfterTakeover(t *testing.T) {
	srv, ctrl, service, source := newExclusiveSessionServe(t)
	runtime, ok := service.Runtime(source)
	if !ok {
		t.Fatal("source runtime is unavailable")
	}
	appendSessionExportTestMessage(t, runtime, "BEFORE-TAKEOVER")
	ts := httptest.NewServer(operatorHandler(srv))
	defer ts.Close()
	snapshot := captureSessionExportTestSnapshot(t, ts.URL, source, false)

	resp, raw := serveBody(t, http.MethodPost, ts.URL+"/handoff", `{"sessionPath":"session-id:`+source.SessionID+`","targetWriterId":"test-taker","force":true,"mode":"wait","timeoutMs":2000}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff status=%d body=%s", resp.StatusCode, raw)
	}
	bodyResp, body := postFixedSessionExport(t, ts.URL, "/session-export/document", source.SessionID, source.SessionID, map[string]any{"snapshot": snapshot, "format": "json"})
	if bodyResp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("BEFORE-TAKEOVER")) {
		t.Fatalf("takeover export status=%d body=%s", bodyResp.StatusCode, body)
	}
	retireExclusiveForeground(t, ctrl, service)
}
