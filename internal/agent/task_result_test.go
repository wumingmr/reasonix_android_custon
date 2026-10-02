package agent

import (
	"strings"
	"testing"
)

func TestSubagentResultWarnsOnHostDecisionLanguage(t *testing.T) {
	out := GuardSubagentHostDecisionText("等待用户批准后再执行修改")
	if !strings.Contains(out, "Subagent boundary") {
		t.Fatalf("guarded output missing boundary warning:\n%s", out)
	}
	plain := "found 3 callers of Foo"
	if got := GuardSubagentHostDecisionText(plain); got != plain {
		t.Fatalf("plain output changed: %q", got)
	}
}
