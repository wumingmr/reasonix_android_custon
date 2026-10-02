package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/tool"
	"reasonix/internal/transcript"
	"strings"
	"testing"
)

func TestSessionLifecycleDiagnosticsBoundedAndContentFree(t *testing.T) {
	controller := &Controller{}
	for i := range 300 {
		controller.recordLifecycle("cancel_requested", "unknown", "turn", uint64(i), "")
	}
	data, err := json.Marshal(controller.lifecycleDiagnosticSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Events  []lifecycleDiagnostic `json:"events"`
		Dropped uint64                `json:"dropped"`
	}
	if err = json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Events) != 256 || view.Dropped != 44 || view.Events[0].Sequence != 44 {
		t.Fatalf("unbounded/miscounted lifecycle trace: %s", data)
	}
	for _, event := range view.Events {
		if event.Source != "unknown" {
			t.Fatal("unknown cancellation was attributed to a user")
		}
	}
}

func TestMCPAttributionNoticeUsesOptionalDisplayEvidence(t *testing.T) {
	controller := &Controller{}
	events, err := controller.sessionEventsFor(event.Event{Kind: event.Notice, Code: event.NoticeCodeMCPToolsList, MessageID: "notice:1", Text: "MCP tools/list", Detail: `{"source":"shared_host","network_call":false}`}, session.Projection{})
	if err != nil || len(events) != 1 || events[0].Kind != "diagnostic" || !events[0].Optional {
		t.Fatalf("display evidence=%+v %v", events, err)
	}
	var payload struct {
		Type   string             `json:"type"`
		Record transcript.Message `json:"displayRecord"`
	}
	if err = json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Type != "display-notice-v1" || payload.Record.Code != event.NoticeCodeMCPToolsList || payload.Record.MessageID != "notice:1" {
		t.Fatalf("lost shared display metadata: %+v", payload)
	}
}

func TestSessionDiagnosticsRetainAcceptedEventsWhenPersistenceFails(t *testing.T) {
	store, err := session.CreateWithOptions(t.TempDir()+"/failed-diagnostic", "failed-diagnostic", session.OpenOptions{Sync: func(*os.File) error { return errors.New("injected disk failure") }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("desktop", failingFlushPersistence{session: store})
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseAll(context.Background())
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "failed-diagnostic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Session().AppendBatch(t.Context(), "accepted", []session.Event{{Kind: "tool/result", Payload: json.RawMessage(`{"id":"tool","name":"bash","output":"ACCEPTED-EVIDENCE"}`)}}); err != nil {
		t.Fatal(err)
	}
	executor := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	controller := newOwnedTestController(t, Options{Executor: executor, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	defer controller.ReleaseResources()
	var data bytes.Buffer
	sensitive := "api" + "_key=sk-proj-" + "1234567890abcdef"
	err = controller.WriteSessionDiagnostics(t.Context(), &data, GoalDiagnosticMetadata{}, map[string]any{"frontendObservation": map[string]string{"error": sensitive}})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data.Bytes()) || !strings.Contains(data.String(), "ACCEPTED-EVIDENCE") || !strings.Contains(data.String(), "durability checkpoint failed") {
		t.Fatalf("lost failure evidence: %s", data.String())
	}
	if strings.Contains(data.String(), sensitive) {
		t.Fatal("diagnostic extension leaked a credential")
	}
	if err = controller.WriteSessionDiagnostics(t.Context(), diagnosticBrokenWriter{}, GoalDiagnosticMetadata{}, nil); err == nil {
		t.Fatal("destination failure reported success")
	}
}

type diagnosticBrokenWriter struct{}

func (diagnosticBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("destination full") }
