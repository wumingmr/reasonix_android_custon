package runtimepolicy

import (
	"testing"

	"reasonix/internal/evidence"
)

func TestParseConstraintsNeverBansMutationFromProse(t *testing.T) {
	for _, instruction := range []string{
		"Do not modify PR #10. Create a branch and implement the repair.",
		"Write AUDIT.md with the result. Do not change any config.",
		"Implement login. The schema: do not modify",
		"Analyze only the payment flow.",
		"Read-only review of PR #10.",
		"Do not modify anything.",
		"No changes.",
		"按方案实现登录模块，数据库结构不要改。",
		"帮我新建登录模块的前后端文件，其他不要改",
		"不要修改当前工作区。",
		"只分析支付流程。",
	} {
		t.Run(instruction, func(t *testing.T) {
			got := ParseConstraints(StripQuotedConstraints(instruction))
			if got.ForbidMutation || !got.AllowsMutation() {
				t.Fatalf("prose set a mutation ban: %+v", got)
			}
			decision := (ConstraintGuard{Constraints: got}).BeforeTool(CallContext{
				Profile: evidence.EffectProfile{Known: true, WorkspaceWrite: true},
			})
			if decision.Action == GuardDeny {
				t.Fatalf("writer decision = %+v, want no constraint denial", decision)
			}
		})
	}
}

func TestPlanModeConstraintDeniesWriters(t *testing.T) {
	c := Constraints{PlanModeReadOnly: true}
	decision := (ConstraintGuard{Constraints: c}).BeforeTool(CallContext{
		Profile: evidence.EffectProfile{Known: true, WorkspaceWrite: true},
	})
	if decision.Action != GuardDeny {
		t.Fatalf("plan-mode writer decision = %+v, want deny", decision)
	}
}

// TestParseConstraintsRecognizesAnExplicitRebuild keeps the rebuild waiver tied
// to the user's own explicit phrasing; nothing else may set it.
func TestParseConstraintsRecognizesAnExplicitRebuild(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"Please rewrite notes.md from scratch.", true},
		{"把 notes.md 完全重写一遍", true},
		{"rewrite the whole file", true},
		{"add a section to notes.md", false},
		{"read notes.md and fix the typo", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := ParseConstraints(tc.text).AllowRebuild; got != tc.want {
			t.Fatalf("ParseConstraints(%q).AllowRebuild = %v, want %v", tc.text, got, tc.want)
		}
	}
}
