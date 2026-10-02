package control

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestGoalDiagnosticExportReadsCompleteDurableV3Log(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "goal-diagnostic"})
	if err != nil {
		t.Fatal(err)
	}
	toolPayload := json.RawMessage(`{"id":"call-1","name":"bash","output":"full diagnostic output; Authorization: Bearer secret-token-123456; api_key=sk-proj-1234567890abcdef\nTOKEN=这是很长的中文测试凭证内容"}`)
	if _, err := runtime.Session().AppendBatch(t.Context(), "tool-evidence", []session.Event{{Kind: "tool/result", Payload: toolPayload}}); err != nil {
		t.Fatal(err)
	}
	machine := goaldomain.NewMachine(nil, func() string { return "goal-1" })
	if _, err := machine.Create(goaldomain.CreateRequest{Objective: "diagnose the goal"}); err != nil {
		t.Fatal(err)
	}
	goalPayload, err := machine.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), "goal:goal-1:1:create", []session.Event{{Kind: "goal/state", Payload: goalPayload}}); err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	t.Cleanup(c.Close)
	c.recordProviderRequest("turn-diagnostic", provider.RequestObservation{ID: 1, Phase: "request_started"})
	c.recordProviderRequest("turn-diagnostic", provider.RequestObservation{ID: 1, Phase: "body_received", BodyBytes: 13740})
	payload, err := c.ExportGoalDiagnostics(t.Context(), GoalDiagnosticMetadata{ApplicationVersion: "1.2.3", BuildCommit: "abc", ProtocolVersion: 4, Capabilities: []string{"goal-lifecycle-v2"}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !utf8.Valid(payload) {
		t.Fatal("diagnostic export contains invalid UTF-8")
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("diagnostic export is not valid JSON: %v\n%s", err, text)
	}
	transport, ok := document["providerDiagnostics"].(map[string]any)
	if !ok {
		t.Fatal("missing provider diagnostics export")
	}
	requests, ok := transport["requests"].([]any)
	if !ok || len(requests) != 1 {
		t.Fatalf("transport requests = %#v", transport["requests"])
	}
	request := requests[0].(map[string]any)
	if request["turnId"] != "turn-diagnostic" || request["phase"] != "body_received" || request["bodyBytes"] != float64(13740) {
		t.Fatalf("transport evidence changed during export: %#v", request)
	}
	for _, want := range []string{`"schemaVersion": 1`, `"applicationVersion": "1.2.3"`, `"sessionCodec": "` + session.Codec + `"`, `"full diagnostic output`, `"goal-lifecycle-v2"`, `"activationChanges"`, `"activation": "armed"`, `"inferred": true`, `"persistenceStatus": "ready"`, `"unavailable"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("diagnostic export missing %s:\n%s", want, text)
		}
	}
	for _, secret := range []string{"secret-token-123456", "sk-proj-1234567890abcdef"} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostic export leaked credential %q", secret)
		}
	}
	if strings.Contains(text, "accepted event traversal failed") {
		t.Fatal("redaction truncated the accepted history")
	}
}

func TestDiagnosticFieldRedactionPreservesNumericCounters(t *testing.T) {
	var output bytes.Buffer
	err := writeGoalDiagnosticField(&output, "test", map[string]any{"token_count": uint64(9007199254740993), "api_key": "secret value"}, false)
	if err != nil || !json.Valid([]byte("{"+output.String()+"}")) || !strings.Contains(output.String(), `"token_count": 9007199254740993`) || strings.Contains(output.String(), "secret value") {
		t.Fatalf("invalid redacted diagnostic field: %s, %v", output.String(), err)
	}
}
