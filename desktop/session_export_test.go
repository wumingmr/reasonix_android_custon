package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"slices"
	"strings"
	"testing"
)

func TestSessionExportCapturesSourceAndCompletePrefix(t *testing.T) {
	app, ref := activityBaselineFixture(t, "export-source")
	runtime, _ := app.desktopSessionService("").Runtime(ref)
	appendMessage := func(id, content string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: content, Origin: provider.MessageOrigin("user")}})
		if _, err := runtime.Session().Append(t.Context(), session.Batch{OperationID: id, Events: []session.Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage("first", "FIRST-QUESTION")
	handle, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &ref}, "", "clipboard", "Source A", `{"residentItems":0}`)
	if err != nil {
		t.Fatal(err)
	}
	defer app.CancelSessionExport(handle.ExportID)
	appendMessage("later", "AFTER-CAPTURE")
	app.activeTabID = "different-tab"
	var body strings.Builder
	var offset int64
	for {
		chunk, err := app.ReadSessionExportChunk(handle.ExportID, offset)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil {
			t.Fatal(err)
		}
		body.Write(bytes)
		offset = chunk.NextOffset
		if chunk.Done {
			break
		}
	}
	if !strings.Contains(body.String(), "FIRST-QUESTION") || strings.Contains(body.String(), "AFTER-CAPTURE") {
		t.Fatalf("unexpected snapshot %q", body.String())
	}
	result, err := app.FinishSessionExport(handle.ExportID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Paths == nil || result.Records != 1 {
		t.Fatalf("result=%+v", result)
	}
	if _, err = app.exportJob(handle.ExportID); err == nil {
		t.Fatal("finished export retained its resources")
	}
}

func TestCancelExportDoesNotCloseSession(t *testing.T) {
	app, ref := activityBaselineFixture(t, "export-cancel")
	handle, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &ref}, "", "clipboard", "Cancel", "")
	if err != nil {
		t.Fatal(err)
	}
	job, err := app.exportJob(handle.ExportID)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.CancelSessionExport(handle.ExportID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(job.dir); !os.IsNotExist(err) {
		t.Fatal("export staging survived cancellation")
	}
	if _, ok := app.desktopSessionService("").Runtime(ref); !ok {
		t.Fatal("export cancelled the session runtime")
	}
	if err = app.CancelSessionExport(handle.ExportID); err != nil {
		t.Fatal("cancel is not idempotent")
	}
}

func TestColdSessionDiagnosticsRetainEvidence(t *testing.T) {
	app, ref := activityBaselineFixture(t, "export-cold")
	if err := app.desktopSessionService("").Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "diagnostic.json")
	host := &recordingNativeHost{dialogPath: path}
	app.setNativeHost(host)
	handle, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &ref}, "", "diagnostic", "Cold", `{"residentItems":0}`)
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
	var value map[string]json.RawMessage
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"metadata", "commits", "activationChanges", "unavailable", "frontendObservation", "sessionIdentity"} {
		if _, ok := value[field]; !ok {
			t.Fatalf("missing %s", field)
		}
	}
}

func TestSessionExportDialogSwitchKeepsSource(t *testing.T) {
	app, ref := activityBaselineFixture(t, "dialog-export")
	runtime, _ := app.desktopSessionService("").Runtime(ref)
	appendMessage := func(id string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: id, Origin: provider.MessageOrigin("user")}})
		if _, err := runtime.Session().AppendBatch(t.Context(), id, []session.Event{{Kind: "message/complete", Payload: data}}); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage("BEFORE-DIALOG")
	path := filepath.Join(t.TempDir(), "export.json")
	app.setNativeHost(&recordingNativeHost{dialogPath: path, onCall: func(name string) {
		if strings.HasPrefix(name, "SaveFileDialog:") {
			app.activeTabID = "B"
			appendMessage("DURING-DIALOG")
		}
	}})
	handle, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &ref}, "", "json", "A", "")
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
	if !json.Valid(data) || !strings.Contains(string(data), "BEFORE-DIALOG") || strings.Contains(string(data), "DURING-DIALOG") {
		t.Fatalf("wrong dialog snapshot: %s", data)
	}
}

func TestSessionExportRejectsCorruptPageWithoutReplacingTarget(t *testing.T) {
	app, ref := activityBaselineFixture(t, "invalid-page")
	path := filepath.Join(t.TempDir(), "export.png")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	app.setNativeHost(&recordingNativeHost{dialogPath: path})
	handle, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &ref}, "", "image", "A", "")
	if err != nil {
		t.Fatal(err)
	}
	defer app.CancelSessionExport(handle.ExportID)
	if err = app.AppendSessionExportPage(handle.ExportID, SessionExportPage{Data: base64.StdEncoding.EncodeToString([]byte("not an image")), Done: true, Width: 1, Height: 1}); err == nil {
		t.Fatal("accepted corrupt page")
	}
	if _, err = app.FinishSessionExport(handle.ExportID); err == nil {
		t.Fatal("published incomplete output")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatal("replaced target on failure")
	}
}

func TestSessionExportRejectsOldRemoteBeforeSaveDialog(t *testing.T) {
	app, tab := remoteRuntimeTestApp(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("old peer must not receive export requests")
		return nil, nil
	})})
	host := &recordingNativeHost{}
	app.setNativeHost(host)
	_, err := app.BeginSessionExportForTarget(SessionSelector{}, tab.id, "json", "Old peer", "")
	if err == nil || !strings.Contains(err.Error(), "session-export-v1") {
		t.Fatalf("missing explicit upgrade error: %v", err)
	}
	if len(host.callNames()) != 0 {
		t.Fatal("opened save dialog for unsupported peer")
	}
}

func TestRemoteSessionExportPinsExplicitIdentityAcrossTabSwitch(t *testing.T) {
	const sourceID = "source-a"
	var requests []string
	app, tab := remoteRuntimeTestApp(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("sessionId"); got != sourceID {
			t.Fatalf("%s sessionId = %q, want %q", req.URL.Path, got, sourceID)
		}
		if got := req.Header.Get(expectedSessionIDHeader); got != sourceID {
			t.Fatalf("%s expected session = %q, want %q", req.URL.Path, got, sourceID)
		}
		requests = append(requests, req.URL.Path)
		switch req.URL.Path {
		case "/session-export/snapshot":
			snapshot := session.ExportSnapshot{Ref: session.SessionRef{HostID: "fixture-host", SessionID: sourceID}, StorageGeneration: "generation-a", SnapshotSequence: 1, AcceptedThrough: 1, DurableThrough: 1, Title: "Source A"}
			return remoteRuntimeTestResponse(req, http.StatusOK, remoteRuntimeTestJSON(t, snapshot)), nil
		case "/session-export/document":
			response := remoteRuntimeTestResponse(req, http.StatusOK, `[{"id":"SOURCE-A"}]`)
			response.Header.Set("X-Reasonix-Export-Records", "1")
			return response, nil
		case "/session-export/validate":
			return remoteRuntimeTestResponse(req, http.StatusNoContent, ""), nil
		default:
			t.Fatalf("unexpected remote export request %s", req.URL)
			return nil, nil
		}
	})})
	tab.capabilities["session-export-v1"] = true
	tab.routing.currentPath = remoteSessionIDRoutePrefix + sourceID
	tab.session.path = tab.routing.currentPath
	path := filepath.Join(t.TempDir(), "remote.json")
	app.setNativeHost(&recordingNativeHost{dialogPath: path, onCall: func(name string) {
		if strings.HasPrefix(name, "SaveFileDialog:") {
			tab.routing.currentPath = remoteSessionIDRoutePrefix + "source-b"
		}
	}})
	ref := session.SessionRef{HostID: "fixture-host", SessionID: sourceID}
	handle, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &ref}, tab.id, "json", "Source A", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.FinishSessionExport(handle.ExportID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("SOURCE-A")) {
		t.Fatalf("published remote export = %s err=%v", data, err)
	}
	want := []string{"/session-export/snapshot", "/session-export/validate", "/session-export/document", "/session-export/validate"}
	if !slices.Equal(requests, want) {
		t.Fatalf("remote requests = %v, want %v", requests, want)
	}
}

func TestSessionExportPublishesValidatedRasterPages(t *testing.T) {
	for _, format := range []string{"pdf", "image"} {
		t.Run(format, func(t *testing.T) {
			app, ref := activityBaselineFixture(t, "raster-"+format)
			extension := ".png"
			if format == "pdf" {
				extension = ".pdf"
			}
			path := filepath.Join(t.TempDir(), "session"+extension)
			app.setNativeHost(&recordingNativeHost{dialogPath: path})
			handle, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &ref}, "", format, "Raster 中文", "")
			if err != nil {
				t.Fatal(err)
			}
			defer app.CancelSessionExport(handle.ExportID)
			var data bytes.Buffer
			pixels := image.NewRGBA(image.Rect(0, 0, 320, 120))
			if format == "pdf" {
				err = jpeg.Encode(&data, pixels, nil)
			} else {
				err = png.Encode(&data, pixels)
			}
			if err != nil {
				t.Fatal(err)
			}
			for index := range 2 {
				if err = app.AppendSessionExportPage(handle.ExportID, SessionExportPage{Index: index, Data: base64.StdEncoding.EncodeToString(data.Bytes()), Done: true, Width: 320, Height: 120}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := app.FinishSessionExport(handle.ExportID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Pages != 2 {
				t.Fatalf("pages=%d", result.Pages)
			}
			if format == "pdf" {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasPrefix(raw, []byte("%PDF-1.4")) || !bytes.Contains(raw, []byte("/Count 2")) || !bytes.HasSuffix(raw, []byte("%%EOF\n")) {
					t.Fatal("invalid PDF structure")
				}
			} else {
				if len(result.Paths) != 2 {
					t.Fatal("wrong PNG count")
				}
				for _, path := range result.Paths {
					file, err := os.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					_, err = png.Decode(file)
					file.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestRemoteExportRejectsReboundSelector(t *testing.T) {
	app, tab := remoteRuntimeTestApp(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("rebound source must not receive requests")
		return nil, nil
	})})
	tab.capabilities = map[string]bool{"session-export-v1": true}
	tab.ref.HostID = "host-B"
	tab.routing.currentPath = "session-id:same-id"
	host := &recordingNativeHost{}
	app.setNativeHost(host)
	_, err := app.BeginSessionExportForTarget(SessionSelector{Ref: &session.SessionRef{HostID: "host-A", SessionID: "same-id"}}, tab.id, "json", "A", "")
	if err == nil {
		t.Fatal("accepted another host with the same session id")
	}
	if len(host.callNames()) != 0 {
		t.Fatal("opened save dialog for a rebound source")
	}
}
