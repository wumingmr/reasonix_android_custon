package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
	"reasonix/internal/session"
	"reasonix/internal/tool"
	"reasonix/internal/transcript"
)

type noDeliverableTransport struct {
	calls   int
	recover bool
}

func (r *noDeliverableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls++
	delta := `"reasoning_content":"fixture thinking"`
	if r.recover && r.calls == 2 {
		delta = `"content":"recovered answer"`
	}
	body := `data: {"choices":[{"delta":{` + delta + `},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}}` + "\n\n" +
		"data: [DONE]\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func TestNoDeliverableSettlementAndColdExport(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		for _, async := range []bool{false, true} {
			name := map[bool]string{false: "failed", true: "recovered"}[recovered] +
				map[bool]string{false: "/sync", true: "/desktop-send"}[async]
			t.Run(name, func(t *testing.T) {
				testNoDeliverableSettlement(t, recovered, async)
			})
		}
	}
}

func testNoDeliverableSettlement(t *testing.T, recovered, async bool) {
	service, err := session.NewService("local", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "no-delivery"})
	if err != nil {
		t.Fatal(err)
	}
	transport := &noDeliverableTransport{recover: recovered}
	p, err := openai.New(provider.Config{Name: "fixture", Protocol: "openai", Model: "fixture-model",
		APIKey: "private-key", BaseURL: "https://provider.test/v1", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(p, tool.NewRegistry(), agent.NewSession("stable system"),
		agent.Options{ModelRef: "fixture/fixture-model"}, event.Discard)
	done := make(chan event.Event, 1)
	c := newOwnedTestController(t, Options{Runner: exec, Executor: exec,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone {
				done <- e
			}
		}), SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	if async {
		c.Send("complete the task")
	} else {
		err = c.RunTurn(t.Context(), "complete the task")
		if recovered && err != nil || !recovered && !errors.Is(err, provider.ErrNoDeliverableOutput) {
			t.Fatalf("RunTurn=%v", err)
		}
	}
	terminal := waitTurnDoneEvent(t, done)
	waitIdleAdmission(t, c)
	wantStatus := event.TurnFailed
	if recovered {
		wantStatus = event.TurnCompleted
	}
	if terminal.Status != wantStatus || transport.calls != 2 {
		t.Fatalf("terminal=%+v, calls=%d", terminal, transport.calls)
	}
	if !recovered && (terminal.Diagnostic == nil || terminal.Diagnostic.Kind != "no_deliverable_output") {
		t.Fatalf("missing live failure classification: %+v", terminal.Diagnostic)
	}
	var summaries []event.ResponseSummary
	for _, commit := range terminationCommitHistory(t, c) {
		for _, e := range commit.Events {
			if e.Kind != "diagnostic/response" {
				continue
			}
			if !e.Optional {
				t.Fatal("response diagnostics must be skippable by older readers")
			}
			var payload struct {
				Response *event.ResponseSummary `json:"response"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Response != nil {
				summaries = append(summaries, *payload.Response)
			}
		}
	}
	if len(summaries) != 2 || summaries[0].FinishReason != "stop" ||
		summaries[0].Usage == nil || summaries[0].Usage.InputTokens != 20 ||
		summaries[0].TextBytes != 0 || summaries[1].RecoveryAttempts != 1 ||
		summaries[1].ModelRef != "fixture/fixture-model" {
		t.Fatalf("response evidence=%+v", summaries)
	}
	if (summaries[1].TextBytes > 0) != recovered {
		t.Fatalf("delivery classification=%+v", summaries[1])
	}
	// Closing and reopening exercises the persisted failure and the old-reader
	// projection: additive attempt metadata must not change message replay.
	ref := runtime.Ref()
	c.Close()
	<-c.closeFinalized
	if err := service.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Query().CaptureDiagnosticSnapshot(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := WriteColdSessionDiagnostics(t.Context(), &out, service.Query(), snapshot, GoalDiagnosticMetadata{}, nil); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"finishReason": "stop"`) ||
		strings.Contains(out.String(), "private-key") {
		t.Fatalf("invalid cold response evidence: %s", out.String())
	}
	var export struct {
		Commits []session.Commit `json:"commits"`
	}
	if err := json.Unmarshal(out.Bytes(), &export); err != nil {
		t.Fatal(err)
	}
	// Use the same canonical reducer as existing session readers.
	projection, err := session.Project(export.Commits)
	if err != nil {
		t.Fatal(err)
	}
	if projection.TurnStatus != wantStatus {
		t.Fatalf("reopened turn status=%s", projection.TurnStatus)
	}
	foundFailure := false
	for _, row := range transcript.History(projection.Messages, transcript.HistoryOptions{}) {
		if row.Diagnostic != nil && row.Diagnostic.Kind == "no_deliverable_output" {
			foundFailure = true
			if row.Level != "warn" || strings.Contains(row.Content, "connection settings") {
				t.Fatalf("misleading reopened notice: %+v", row)
			}
		}
	}
	if foundFailure == recovered {
		t.Fatalf("reopened failure notice present=%v, recovered=%v", foundFailure, recovered)
	}
}
