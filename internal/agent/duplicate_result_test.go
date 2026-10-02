package agent

import (
	"strings"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestProviderVisibleResultKeepsExactRepeats(t *testing.T) {
	a := &Agent{}
	first, _, _ := a.boundProviderVisibleResult("hello world", "read_file", "c1")
	second, _, _ := a.boundProviderVisibleResult("hello world", "read_file", "c2")
	if first != "hello world" || second != "hello world" {
		t.Fatalf("repeated results were rewritten: first=%q second=%q", first, second)
	}
}

func TestTodoWriteResultsAreNeverPresentationDeduplicated(t *testing.T) {
	a := &Agent{}
	raw := `{"todos":[{"content":"ship","status":"completed"}],"counts":{"total":1,"pending":0,"in_progress":0,"completed":1}}`
	first, _, _ := a.boundProviderVisibleResult(raw, "todo_write", "todo-1")
	second, _, _ := a.boundProviderVisibleResult(raw, "todo_write", "todo-2")
	if first != raw || second != raw {
		t.Fatalf("todo result was rewritten: first=%q second=%q", first, second)
	}
}

func TestResolvedSkipOutcomeKeepsRepeatedLocalDiscovery(t *testing.T) {
	a := &Agent{}
	result := `{"id":"mcp-tool:server/read","input_schema":{"type":"object"}}`
	plan := func(id string) *toolCallPlan {
		return &toolCallPlan{call: provider.ToolCall{ID: id, Name: "use_capability", Arguments: `{}`}}
	}
	resolved := tool.ResolvedCall{ProxyAction: "inspect", SkipExecute: true, ReadOnly: true, Result: result}
	first := a.resolvedSkipOutcome(plan("c1"), resolved)
	if first.output != result {
		t.Fatalf("first output = %q", first.output)
	}
	second := a.resolvedSkipOutcome(plan("c2"), resolved)
	if second.output != result {
		t.Fatalf("second output = %q", second.output)
	}
	if second.rawOutput != "" {
		t.Fatalf("untransformed complete output should not need a duplicate local copy: %q", second.rawOutput)
	}
}

func TestSummarizeCIOutputKeepsFailures(t *testing.T) {
	body := "##teamcity[testFailed name='A']\n" + strings.Repeat("ok\n", 400) + "exit status 1\n"
	got := summarizeCIOutput(body)
	if !strings.Contains(got, "exit_code: 1") || !strings.Contains(got, "testFailed") {
		t.Fatalf("summary = %q", got)
	}
	if len(got) >= len(body) {
		t.Fatal("summary should be smaller than the raw CI log")
	}
}
