package control

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/skill"
)

type goalWorkCallbackRunner struct{ run func() error }

func (r goalWorkCallbackRunner) Run(context.Context, string) error { return r.run() }

func TestGoalWorkObservationFailurePreservesRunResult(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		name   string
		result error
	}{
		{"success", nil}, {"cancelled", context.Canceled}, {"provider-error", errors.New("provider failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			c, runtime := goalWorkController(t)
			c.runner = goalWorkCallbackRunner{run: func() error {
				if err := runtime.Session().Handle().Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				return tc.result
			}}
			// errors.Is alone would accept an added errors.Join; compare exact return identity.
			if err := c.runModelTurn(t.Context(), "work"); reflect.ValueOf(err) != reflect.ValueOf(tc.result) {
				t.Fatalf("observational write changed result: got %v, want %v", err, tc.result)
			}
			if !strings.Contains(logs.String(), "persist goal work duration") {
				t.Fatalf("missing persistence warning: %s", logs.String())
			}
			if got := c.GoalRuntime().WorkDurationMs; got != 0 {
				t.Fatalf("uncommitted duration published: %d", got)
			}
		})
	}
}

func TestGoalWorkObservationFailurePreservesSubagentResult(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		name   string
		result error
	}{
		{"success", nil}, {"cancelled", context.Canceled}, {"provider-error", errors.New("child failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			c, runtime := goalWorkController(t)
			c.sink = event.FuncSink(func(e event.Event) {
				// The final answer is already recorded before Message; failed child Runs
				// emit ToolResult before returning their original error.
				if e.Kind == event.Message || (e.Kind == event.ToolResult && e.Tool.Err != "") {
					if err := runtime.Session().Handle().Close(t.Context()); err != nil {
						t.Error(err)
					}
				}
			})
			runner := func(context.Context, skill.Skill, string, skill.SubagentRunOptions) (string, error) {
				return "answer", tc.result
			}
			err := newTurnOrchestrator(c).runSubagentSkillTurns(t.Context(), []skill.Skill{{Name: "research", Body: "inspect", RunAs: skill.RunSubagent}}, "work", "work", "work", runner, false, nil, nil)
			if reflect.ValueOf(err) != reflect.ValueOf(tc.result) {
				t.Fatalf("observational write changed child result: got %v, want %v", err, tc.result)
			}
			if !strings.Contains(logs.String(), "persist goal work duration") {
				t.Fatalf("missing persistence warning: %s", logs.String())
			}
			if got := c.GoalRuntime().WorkDurationMs; got != 0 {
				t.Fatalf("uncommitted duration published: %d", got)
			}
		})
	}
}
