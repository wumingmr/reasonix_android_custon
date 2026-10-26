package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/provider/responses"
	"reasonix/internal/tool"
)

type terminalExpectation struct {
	requests          int32
	outputs           map[string]string
	finals            int
	final             string
	cause             error
	streamInterrupted bool
	notice            string
	noNotices         bool
	isolateCause      bool
}

func runTerminalFixture(t *testing.T, events []string, want terminalExpectation) {
	t.Helper()
	var requests, executions atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if n > 1 {
			assertTerminalOutputs(t, body, want.outputs)
			if want.isolateCause && strings.Contains(string(body), want.cause.Error()) {
				t.Error("protocol error cause reached model request")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n > 1 {
			_, _ = io.WriteString(w, responsesFinalAnswerSSE)
			return
		}
		for _, s := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", s)
		}
	}))
	defer srv.Close()
	p := responses.New(responses.Config{Name: "mimo-diagnostic", APIKey: "fake-local-only", BaseURL: "https://api.xiaomimimo.com/v1", RequestURL: srv.URL, Model: "mimo-v2.6-flash", Effort: "high"})
	reg := tool.NewRegistry()
	reg.Add(terminalEchoTool{calls: &executions})
	reg.Add(terminalReadyTool{calls: &executions})
	sink := &recordSink{}
	a := New(p, reg, NewSession(""), Options{MissingReasoningWarnStateDir: t.TempDir()}, sink)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := a.Run(ctx, "执行所需工具，然后报告结果")
	if !errors.Is(err, want.cause) {
		t.Errorf("Run error=%v, want cause %v", err, want.cause)
	}
	if want.cause != nil && !errors.Is(fmt.Errorf("caller: %w", err), want.cause) {
		t.Error("wrapped Run error lost identity")
	}
	if provider.IsStreamInterrupted(err) != want.streamInterrupted {
		t.Errorf("stream interruption=%v want=%v", provider.IsStreamInterrupted(err), want.streamInterrupted)
	}
	if want.isolateCause && provider.ClassifyRecovery(err).Retryable {
		t.Error("protocol error became retryable")
	}
	if requests.Load() != want.requests {
		t.Errorf("requests=%d want=%d", requests.Load(), want.requests)
	}
	if executions.Load() != int32(len(want.outputs)) {
		t.Errorf("actual executions=%d want=%d", executions.Load(), len(want.outputs))
	}
	if len(sink.kinds(event.Retrying)) != 0 {
		t.Error("unexpected automatic retry")
	}
	gotIDs := map[string]int{}
	for _, e := range sink.kinds(event.ToolResult) {
		gotIDs[e.Tool.ID]++
	}
	if len(gotIDs) != len(want.outputs) {
		t.Errorf("tool results=%v want=%v", gotIDs, want.outputs)
	}
	for id := range want.outputs {
		if gotIDs[id] != 1 {
			t.Errorf("call %s result count=%d want=1", id, gotIDs[id])
		}
	}
	var finals []string
	calls := map[string]int{}
	for _, m := range a.Session().Snapshot() {
		if m.LocalOnly {
			continue
		}
		if want.isolateCause && strings.Contains(m.Content, want.cause.Error()) {
			t.Error("protocol error cause committed to model history")
		}
		if m.Role == provider.RoleAssistant {
			finals = append(finals, m.Content)
			for _, call := range m.ToolCalls {
				calls[call.ID]++
			}
		}
	}
	if !reflect.DeepEqual(calls, gotIDs) {
		t.Errorf("committed calls=%v tool results=%v", calls, gotIDs)
	}
	if len(finals) != want.finals || (len(finals) > 0 && finals[len(finals)-1] != want.final) {
		t.Errorf("finals=%q want count=%d final=%q", finals, want.finals, want.final)
	}
	notices := sink.kinds(event.Notice)
	var texts []string
	for _, e := range notices {
		texts = append(texts, e.Text)
	}
	if want.noNotices && len(notices) > 0 {
		t.Errorf("unexpected notices=%q", texts)
	}
	if want.notice != "" && !strings.Contains(strings.Join(texts, " "), want.notice) {
		t.Errorf("notices=%q missing %q", texts, want.notice)
	}
	if want.isolateCause {
		// Explicit user continuation is separate from the refused attempt: it must
		// remain possible without leaking the host's error cause into model input.
		if err := a.Run(ctx, "继续"); err != nil {
			t.Fatalf("explicit continuation: %v", err)
		}
		if requests.Load() != want.requests+1 {
			t.Errorf("explicit continuation requests=%d", requests.Load())
		}
	}
}

func assertTerminalOutputs(t *testing.T, body []byte, want map[string]string) {
	t.Helper()
	var request struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Error(err)
		return
	}
	got := map[string]string{}
	counts := map[string]int{}
	for _, item := range request.Input {
		if item.Type == "function_call_output" {
			got[item.CallID] = item.Output
			counts[item.CallID]++
		}
	}
	if len(got) != len(want) {
		t.Errorf("continuation outputs=%v want=%v", got, want)
	}
	for id, output := range want {
		if counts[id] != 1 || got[id] != output {
			t.Errorf("continuation call=%s count=%d output=%q want=%q", id, counts[id], got[id], output)
		}
	}
}

type terminalEchoTool struct {
	echoTool
	calls *atomic.Int32
}

func (t terminalEchoTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	t.calls.Add(1)
	return t.echoTool.Execute(ctx, args)
}

type terminalReadyTool struct{ calls *atomic.Int32 }

func (terminalReadyTool) Name() string        { return "ready" }
func (terminalReadyTool) Description() string { return "report readiness without arguments" }
func (terminalReadyTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (terminalReadyTool) ReadOnly() bool { return true }
func (t terminalReadyTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.calls.Add(1)
	return "ready", nil
}
