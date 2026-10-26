package agent

import (
	"io"
	"strings"
	"testing"

	"reasonix/internal/provider/responses"
)

// Fixtures exercise the real Responses adapter and Agent without a live model.
func TestResponsesTerminalToolIntegrity(t *testing.T) {
	const text = `{"type":"response.output_text.delta","item_id":"msg_1","content_index":0,"delta":"现在打开浏览器访问 ComfyUI："}`
	const start = `{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"echo","arguments":""}}`
	const args = `{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"text\":\"hi\"}"}`
	const partial = `{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"text\":"}`
	const done = `{"type":"response.function_call_arguments.done","item_id":"fc_1","name":"echo","arguments":"{\"text\":\"hi\"}"}`
	const itemDone = `{"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"echo","status":"completed","arguments":"{\"text\":\"hi\"}"}}`
	const complete = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"现在打开浏览器访问 ComfyUI：","annotations":[]}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"echo","status":"completed","arguments":"{\"text\":\"hi\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`
	const noCall = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"现在打开浏览器访问 ComfyUI：","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`
	const incomplete = `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"现在打开浏览器访问 ComfyUI：","annotations":[]}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"echo","status":"incomplete","arguments":"{\"text\":"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`
	const malformed = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"现在打开浏览器访问 ComfyUI：","annotations":[]}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"echo","status":"completed","arguments":{"text":"hi"}}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`

	parallel := strings.Replace(complete, `],"usage"`, `,{"id":"fc_2","type":"function_call","call_id":"call_2","name":"echo","status":"completed","arguments":"{\"text\":\"second\"}"}],"usage"`, 1)
	second := strings.NewReplacer("fc_1", "fc_2", "call_1", "call_2", "hi", "second")
	noArgs := `{"type":"response.completed","response":{"id":"r","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ready","status":"completed","arguments":"{}"}]}}`
	success := terminalExpectation{requests: 2, outputs: map[string]string{"call_1": "echoed: hi"}, finals: 2, final: "done"}
	textOnly := terminalExpectation{requests: 1, finals: 1, final: "现在打开浏览器访问 ComfyUI：", noNotices: true}
	invalid := terminalExpectation{requests: 1, cause: responses.ErrInvalidFunctionCall, isolateCause: true}
	unfinished := terminalExpectation{requests: 1, cause: responses.ErrUnfinishedFunctionCall, isolateCause: true}
	cases := []struct {
		name   string
		events []string
		want   terminalExpectation
	}{
		{"schema_shaped_tool_lifecycle", []string{`{"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`, `{"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress","output":[]}}`, `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`, text, `{"type":"response.output_text.done","item_id":"msg_1","content_index":0,"text":"现在打开浏览器访问 ComfyUI："}`, `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"现在打开浏览器访问 ComfyUI：","annotations":[]}]}}`, start, args, done, itemDone, complete}, success},
		{"completed_output_missing_done_events", []string{text, start, args, complete}, success},
		{"parallel_legitimate_calls", []string{text, start, second.Replace(start), args, second.Replace(args), done, second.Replace(done), itemDone, second.Replace(itemDone), parallel}, terminalExpectation{requests: 2, outputs: map[string]string{"call_1": "echoed: hi", "call_2": "echoed: second"}, finals: 2, final: "done"}},
		{"transition_text_without_calls", []string{text, noCall}, textOnly},
		{"partial_call_then_DONE", []string{text, start, partial, "[DONE]"}, unfinished},
		{"partial_call_then_completed", []string{text, start, partial, noCall}, unfinished},
		{"partial_call_then_incomplete_max_output", []string{text, start, partial, incomplete}, terminalExpectation{requests: 1, finals: 1, final: "现在打开浏览器访问 ComfyUI：", notice: "response truncated"}},
		{"partial_call_then_EOF", []string{text, start, partial}, terminalExpectation{requests: 1, cause: io.ErrUnexpectedEOF, streamInterrupted: true, notice: "stream ended before completion"}},
		{"malformed_arguments_object_in_terminal", []string{text, start, args, malformed}, invalid},
		{"malformed_terminal_without_start", []string{text, malformed}, invalid},
		{"completed_call_before_malformed_terminal", []string{text, start, args, done, itemDone, strings.Replace(malformed, `"call_id":"call_1"`, `"call_id":"call_2"`, 1)}, invalid},
		{"no_argument_tool_with_empty_object", []string{text, noArgs}, terminalExpectation{requests: 2, outputs: map[string]string{"call_1": "ready"}, finals: 2, final: "done"}},
		{"no_argument_tool_omitting_arguments", []string{text, strings.Replace(noArgs, `,"arguments":"{}"`, "", 1)}, invalid},
		{"completed_item_still_in_progress", []string{text, strings.Replace(complete, `"name":"echo","status":"completed"`, `"name":"echo","status":"in_progress"`, 1)}, invalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runTerminalFixture(t, tc.events, tc.want) })
	}
}
