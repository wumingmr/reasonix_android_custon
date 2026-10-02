package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	goaldomain "reasonix/internal/goal"
	"reasonix/internal/tool"
)

func TestUpdateGoalLifecycleActionsAcceptInactiveFields(t *testing.T) {
	for _, action := range []tool.GoalAction{tool.GoalActionResume, tool.GoalActionPause, tool.GoalActionComplete} {
		for _, fields := range []string{
			``,
			`,"objective":"ship","max_goal_rounds":null,"blocked_reason":""`,
			`,"objective":"","max_goal_rounds":null,"blocked_reason":""`,
			`,"objective":null,"max_goal_rounds":null,"blocked_reason":null`,
			`,"objective":"different objective","max_goal_rounds":20,"blocked_reason":"old blocker"`,
		} {
			t.Run(string(action)+fields, func(t *testing.T) {
				args := json.RawMessage(fmt.Sprintf(`{"goal_id":"goal-1","revision":3,"action":%q%s}`, action, fields))
				validation := tool.ValidateArguments(updateGoal{}, args)
				if validation.CompileErr != nil || len(validation.Violations) != 0 || validation.Skipped {
					t.Fatalf("host argument validation = %+v", validation)
				}
				stub := &goalLifecycleStub{view: goalView()}
				ctx := goalLifecycleContext(stub, tool.GoalSourceDirectHuman)
				if _, err := (updateGoal{}).Execute(ctx, args); err != nil {
					t.Fatal(err)
				}
				want := tool.GoalUpdateRequest{Ref: goaldomain.Ref{ID: "goal-1", Revision: 3}, Action: action}
				if stub.updateRequest != want {
					t.Fatalf("inactive fields leaked into mutation: %+v", stub.updateRequest)
				}
				binding, _ := tool.GoalLifecycleFromContext(ctx)
				if stub.authority != binding.Authority {
					t.Fatalf("authority changed: %+v", stub.authority)
				}
			})
		}
	}
}

func TestUpdateGoalEditAndBlockedConsumeOnlyActiveFields(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		check      func(*testing.T, tool.GoalUpdateRequest)
	}{
		{"edit", `{"goal_id":"goal-1","revision":3,"action":"edit","objective":" ship safely ","blocked_reason":"old blocker"}`, func(t *testing.T, got tool.GoalUpdateRequest) {
			if got.Objective == nil || *got.Objective != "ship safely" || got.MaxGoalRounds.Set || got.BlockedReason != nil {
				t.Fatalf("edit request = %+v", got)
			}
		}},
		{"blocked", `{"goal_id":"goal-1","revision":3,"action":"blocked","objective":"old objective","max_goal_rounds":null,"blocked_reason":" dependency unavailable "}`, func(t *testing.T, got tool.GoalUpdateRequest) {
			if got.Objective != nil || got.MaxGoalRounds.Set || got.BlockedReason == nil || got.BlockedReason.Message != "dependency unavailable" {
				t.Fatalf("blocked request = %+v", got)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &goalLifecycleStub{view: goalView()}
			if _, err := (updateGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceDirectHuman), json.RawMessage(tc.args)); err != nil {
				t.Fatal(err)
			}
			tc.check(t, stub.updateRequest)
		})
	}
}

func TestUpdateGoalInactiveFieldsPreserveOwnerRejections(t *testing.T) {
	for _, code := range []goaldomain.ErrorCode{goaldomain.ErrStaleRevision, goaldomain.ErrUserAuthorityRequired, goaldomain.ErrInvalidTransition} {
		t.Run(string(code), func(t *testing.T) {
			stub := &goalLifecycleStub{view: goalView(), err: &goaldomain.Error{Code: code, Message: "rejected by owner"}}
			result, err := (updateGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceDirectHuman), json.RawMessage(`{"goal_id":"goal-1","revision":3,"action":"resume","objective":"ship","max_goal_rounds":null,"blocked_reason":""}`))
			if !errors.Is(err, stub.err) || goaldomain.ErrorCodeOf(err) != code {
				t.Fatalf("owner error was lost: %v", err)
			}
			if result != "" {
				t.Fatalf("rejected action returned a success result: %s", result)
			}
		})
	}
}

func TestUpdateGoalReportsUnusedFields(t *testing.T) {
	for _, tc := range []struct {
		name, action, fields, ignored string
	}{
		{"resume", "resume", `,"objective":"other target","max_goal_rounds":20,"blocked_reason":"old blocker"`, "objective, max_goal_rounds, blocked_reason"},
		{"pause", "pause", `,"objective":"other target","max_goal_rounds":20`, "objective, max_goal_rounds"},
		{"complete", "complete", `,"objective":"other target","max_goal_rounds":20`, "objective, max_goal_rounds"},
		{"edit", "edit", `,"objective":"new target","blocked_reason":"old blocker"`, "blocked_reason"},
		{"blocked", "blocked", `,"objective":"old target","max_goal_rounds":null,"blocked_reason":"dependency missing"`, "objective, max_goal_rounds"},
		{"empty placeholders", "resume", `,"objective":"","max_goal_rounds":null,"blocked_reason":""`, "objective, max_goal_rounds, blocked_reason"},
		{"null placeholders", "resume", `,"objective":null,"max_goal_rounds":null,"blocked_reason":null`, "max_goal_rounds"},
		{"omitted", "resume", ``, ""},
		{"active fields only", "edit", `,"objective":"new target","max_goal_rounds":null`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := goalView()
			switch tc.action {
			case "complete":
				view.Phase = goaldomain.PhaseComplete
			case "blocked":
				view.Phase = goaldomain.PhaseBlocked
			}
			stub := &goalLifecycleStub{view: view}
			args := json.RawMessage(fmt.Sprintf(`{"goal_id":"goal-1","revision":3,"action":%q%s}`, tc.action, tc.fields))
			result, err := (updateGoal{}).Execute(goalLifecycleContext(stub, tool.GoalSourceDirectHuman), args)
			if err != nil {
				t.Fatal(err)
			}
			var got goalToolValue
			if err := json.Unmarshal([]byte(result), &got); err != nil {
				t.Fatal(err)
			}
			if tc.ignored == "" {
				if got.Instruction != "" {
					t.Fatalf("unexpected instruction: %q", got.Instruction)
				}
			} else if !strings.Contains(got.Instruction, "Fields not applied: "+tc.ignored+".") {
				t.Fatalf("instruction = %q, want ignored fields %q", got.Instruction, tc.ignored)
			}
			if (tc.action == "complete" || tc.action == "blocked") && !strings.HasPrefix(got.Instruction, "Finish the current turn") {
				t.Fatalf("terminal instruction lost: %q", got.Instruction)
			}
			if got.Goal == nil || got.Goal.Objective != view.Objective || got.Goal.Phase != view.Phase {
				t.Fatalf("effective goal state lost: %+v", got.Goal)
			}
		})
	}
}
