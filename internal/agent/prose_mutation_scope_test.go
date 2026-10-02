package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func runOneWrite(t *testing.T, input string, planMode bool) (int32, string) {
	t.Helper()
	var calls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "write_file", calls: &calls})
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "w1", Name: "write_file", Arguments: `{"path":"backend/login.go","content":"package backend"}`}}},
		testutil.Turn{Text: "done"},
	)
	a := New(mp, reg, NewSession(""), Options{}, event.Discard)
	a.SetPlanMode(planMode)
	if err := a.Run(withNoClosedLoop(context.Background()), input); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return atomic.LoadInt32(&calls), lastToolResult(a.Session(), "write_file")
}

// The user's prose reaches the model as an instruction; only plan mode is a
// host-enforced read-only switch.
func TestUserProseDoesNotBanWritesForTheTurn(t *testing.T) {
	for _, input := range []string{
		"按方案实现登录模块，数据库结构不要改。",
		"帮我新建登录模块的前后端文件，其他不要改",
		"实现登录功能。接口不要修改",
		"Implement login. The schema: do not modify",
		"Do not modify anything else.",
		"只分析支付流程。",
	} {
		t.Run(input, func(t *testing.T) {
			calls, result := runOneWrite(t, input, false)
			if calls != 1 {
				t.Fatalf("write_file executed %d times, want 1; result=%q", calls, result)
			}
		})
	}
}

func TestPlanModeStillBlocksWrites(t *testing.T) {
	calls, result := runOneWrite(t, "实现登录功能", true)
	if calls != 0 {
		t.Fatalf("write_file executed %d times in plan mode, want 0; result=%q", calls, result)
	}
}
