package runtimepolicy

import (
	"encoding/json"
	"strings"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

// PlanGuard hard-blocks writes while Plan mode is active, including YOLO.
type PlanGuard struct{}

func (PlanGuard) BeforeTool(ctx CallContext) GuardDecision {
	if !ctx.PlanReadOnly || !ctx.Profile.MutatesState() {
		return GuardDecision{Action: GuardAbstain}
	}
	return GuardDecision{
		Action:  GuardDeny,
		Reasons: []string{"plan_boundary"},
		Message: "blocked: plan mode forbids workspace mutations until the plan is approved",
	}
}
func (PlanGuard) AfterTool(ResultContext) []evidence.Receipt { return nil }

// ConstraintGuard applies explicit user/host limits only.
type ConstraintGuard struct{ Constraints Constraints }

func (g ConstraintGuard) BeforeTool(ctx CallContext) GuardDecision {
	c := g.Constraints
	if ctx.Profile.MutatesState() && !c.AllowsMutation() {
		return GuardDecision{
			Action:  GuardDeny,
			Reasons: []string{"user_constraint"},
			Message: "blocked: the current constraints forbid state mutation",
		}
	}
	if (ctx.Profile.ExternalState || looksExternalCommand(ctx)) && !c.AllowsExternal() {
		return GuardDecision{
			Action:  GuardDeny,
			Reasons: []string{"user_constraint"},
			Message: "blocked: the current constraints forbid push/publish/deploy-style actions",
		}
	}
	if ctx.Verification && !c.AllowsTests() {
		return GuardDecision{
			Action:  GuardDeny,
			Reasons: []string{"user_constraint"},
			Message: "blocked: the current constraints forbid verification commands",
		}
	}
	if ctx.Verification && !c.AllowsCommand(bashCommand(ctx)) {
		return GuardDecision{
			Action:  GuardDeny,
			Reasons: []string{"user_constraint"},
			Message: "blocked: verification command is outside the user allowlist",
		}
	}
	return GuardDecision{Action: GuardAbstain}
}
func (ConstraintGuard) AfterTool(ResultContext) []evidence.Receipt { return nil }

func bashCommand(ctx CallContext) string {
	name := strings.ToLower(strings.TrimSpace(ctx.ToolName))
	if !tool.IsShellToolName(name) {
		return ""
	}
	var payload struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(ctx.Args, &payload) == nil {
		return strings.TrimSpace(payload.Command)
	}
	return ""
}

func looksExternalCommand(ctx CallContext) bool {
	name := strings.ToLower(strings.TrimSpace(ctx.ToolName))
	if strings.Contains(name, "deploy") || strings.Contains(name, "publish") || strings.Contains(name, "push") {
		return true
	}
	cmd := strings.ToLower(bashCommand(ctx))
	if cmd == "" {
		return false
	}
	for _, needle := range []string{"git push", "publish", "kubectl", "deploy", "helm push"} {
		if strings.Contains(cmd, needle) {
			return true
		}
	}
	return false
}

// MutationDependencyGuard blocks later mutations after an earlier batch failure.
type MutationDependencyGuard struct{ Blocked bool }

func (g MutationDependencyGuard) BeforeTool(ctx CallContext) GuardDecision {
	if !g.Blocked || (!ctx.Profile.MutatesState() && !ctx.Verification) {
		return GuardDecision{Action: GuardAbstain}
	}
	return GuardDecision{
		Action:  GuardDeny,
		Reasons: []string{"receipt"},
		Message: "blocked: an earlier mutation in this batch failed; later mutations and verifications cannot run",
	}
}
func (MutationDependencyGuard) AfterTool(ResultContext) []evidence.Receipt { return nil }

// OpaqueWriterGuard asks when an unknown writer can be reviewed, else denies.
type OpaqueWriterGuard struct{}

func (OpaqueWriterGuard) BeforeTool(ctx CallContext) GuardDecision {
	name := strings.ToLower(strings.TrimSpace(ctx.ToolName))
	if !ctx.Profile.OpaqueWriter() || tool.IsShellToolName(name) {
		return GuardDecision{Action: GuardAbstain}
	}
	if ctx.Interactive {
		return GuardDecision{
			Action:  GuardAsk,
			Reasons: []string{"opaque_writer"},
			Message: "unknown writer requires explicit approval",
		}
	}
	return GuardDecision{
		Action:  GuardDeny,
		Reasons: []string{"opaque_writer"},
		Message: "blocked: unknown writer cannot run without an interactive approval channel",
	}
}
func (OpaqueWriterGuard) AfterTool(ResultContext) []evidence.Receipt { return nil }
