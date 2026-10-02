package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"reasonix/internal/provider"
)

type localOpenCodeGoTransport struct {
	target *url.URL
}

func (t localOpenCodeGoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = t.target.Scheme, t.target.Host
	req.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestOpenCodeGoChatToolReplayOmitsToolName(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if r.URL.Path != "/zen/go/v1/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		var body struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if requestNumber == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"bash\",\"arguments\":\"{}\"}}]}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		for i, message := range body.Messages {
			if string(message["role"]) != `"tool"` {
				continue
			}
			if _, ok := message["name"]; ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"error":{"message":"messages[%d]: name is not supported by this endpoint"}}`, i)
				return
			}
			if string(message["tool_call_id"]) != `"call_1"` || string(message["content"]) != `"ok"` {
				t.Errorf("tool result = %v", message)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		t.Error("second request missing tool result")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(provider.Config{
		Name: "opencode-go", BaseURL: "https://opencode.ai/zen/go/v1", Model: "deepseek-v4-flash", APIKey: "test",
		HTTPClient: &http.Client{Transport: localOpenCodeGoTransport{target: target}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []provider.Message{{Role: provider.RoleUser, Content: "run bash"}}
	first, err := p.Stream(t.Context(), provider.Request{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	var call *provider.ToolCall
	for chunk := range first {
		if chunk.Type == provider.ChunkError {
			t.Fatal(chunk.Err)
		}
		if chunk.Type == provider.ChunkToolCall {
			call = chunk.ToolCall
		}
	}
	if call == nil || call.ID != "call_1" || call.Name != "bash" {
		t.Fatalf("first turn tool call = %+v", call)
	}
	messages = append(messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{*call}},
		provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: "ok"},
	)
	second, err := p.Stream(t.Context(), provider.Request{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for chunk := range second {
		if chunk.Type == provider.ChunkError {
			t.Fatal(chunk.Err)
		}
		if chunk.Type == provider.ChunkText {
			text.WriteString(chunk.Text)
		}
	}
	if requests.Load() != 2 || text.String() != "done" {
		t.Fatalf("requests = %d, second response = %q", requests.Load(), text.String())
	}
}

func TestOpenCodeGoToolNameOnlyOmittedOnOfficialChatRoute(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, requestURL string
		wantName                  bool
	}{
		{name: "official chat", baseURL: "https://opencode.ai/zen/go/v1", wantName: false},
		{name: "official request override", baseURL: "https://gateway.example/v1", requestURL: "https://opencode.ai/zen/go/v1/chat/completions", wantName: false},
		{name: "custom request override", baseURL: "https://opencode.ai/zen/go/v1", requestURL: "https://gateway.example/v1/chat/completions", wantName: true},
		{name: "other endpoint", baseURL: "https://api.xiaomimimo.com/v1", wantName: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{}
			if tc.requestURL != "" {
				extra["request_url"] = tc.requestURL
			}
			p, err := New(provider.Config{BaseURL: tc.baseURL, Model: "deepseek-v4-flash", Extra: extra})
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(p.(*client).buildRequest(provider.Request{Messages: []provider.Message{
				{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "bash", Arguments: "{}"}}},
				{Role: provider.RoleTool, ToolCallID: "call_1", Name: "bash", Content: "ok"},
			}}))
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Messages []map[string]json.RawMessage `json:"messages"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			_, hasName := wire.Messages[1]["name"]
			if hasName != tc.wantName {
				t.Fatalf("tool name present = %v, want %v: %s", hasName, tc.wantName, body)
			}
		})
	}
}

func TestOpenCodeGoDeepSeekWireKeepsReasoningAndStableHistory(t *testing.T) {
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"} {
		p, err := New(provider.Config{BaseURL: "https://opencode.ai/zen/go/v1", Model: model, Extra: map[string]any{"request_url": "https://opencode.ai/zen/go/v1/chat/completions", "thinking": "enabled", "effort": "max"}})
		if err != nil {
			t.Fatal(err)
		}
		c := p.(*client)
		req := provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "fixture"}, {Role: provider.RoleAssistant, ReasoningContent: "provider-reasoning", ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "read_file", Arguments: "{}"}}}, {Role: provider.RoleTool, ToolCallID: "call-1", Content: "tool-result"}}, MaxTokens: 128}
		body := c.buildRequest(req)
		first, _ := json.Marshal(body)
		second, _ := json.Marshal(c.buildRequest(req))
		if !bytes.Equal(first, second) || !bytes.Contains(first, []byte(`"reasoning_effort":"max"`)) || !bytes.Contains(first, []byte(`"reasoning_content":"provider-reasoning"`)) {
			t.Fatalf("%s lost effort/replay/cache stability: %s", model, first)
		}
	}
}
