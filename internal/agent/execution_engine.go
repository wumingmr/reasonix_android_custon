package agent

import (
	"context"
	"encoding/json"

	"reasonix/internal/evidence"
	"reasonix/internal/runtimepolicy"
	"reasonix/internal/tool"
)

// withInheritedHostConstraints re-applies the spawning turn's host constraints
// to a background job context. Jobs run on a root context, so without this a
// child would re-derive its constraints from the model-authored task prompt.
func withInheritedHostConstraints(parent, job context.Context) context.Context {
	if c, ok := runtimepolicy.FromContext(parent); ok {
		job = runtimepolicy.WithContext(job, c)
	}
	if in, ok := runtimepolicy.InheritedFromContext(parent); ok {
		job = runtimepolicy.WithInherited(job, in)
	}
	return job
}

func mergeInheritedConstraints(child, parent runtimepolicy.Constraints) runtimepolicy.Constraints {
	if parent.ForbidMutation {
		child.ForbidMutation = true
	}
	if parent.ForbidTests {
		child.ForbidTests = true
	}
	if parent.ForbidExternal {
		child.ForbidExternal = true
	}
	if parent.PlanModeReadOnly {
		child.PlanModeReadOnly = true
		child.ForbidMutation = true
	}
	if len(parent.AllowedChecks) > 0 && len(child.AllowedChecks) == 0 {
		child.AllowedChecks = append([]string(nil), parent.AllowedChecks...)
	}
	if len(parent.RebuildPaths) > 0 && len(child.RebuildPaths) == 0 {
		child.RebuildPaths = append([]string(nil), parent.RebuildPaths...)
	}
	return child
}

func (a *Agent) pipelineDecision(plan *toolCallPlan) runtimepolicy.GuardDecision {
	if a == nil || plan == nil || a.turn.engine == nil {
		return runtimepolicy.GuardDecision{}
	}
	profile := evidence.ClassifyEffect(evidence.EffectInput{
		ToolName:       plan.evidenceName,
		Args:           plan.evidenceArgs,
		StaticReadOnly: plan.readOnly,
		Hint:           effectHintOf(plan.execTool, plan.execArgs),
		ActualPaths:    evidence.ToolCallPaths(plan.evidenceArgs),
		WorkspaceRoot:  a.writeWorkspaceRoot,
	})
	plan.profile = profile
	plan.effects = profile.ToolEffects()
	return a.turn.engine.BeforeTool(runtimepolicy.CallContext{
		ToolName:       plan.evidenceName,
		Args:           plan.evidenceArgs,
		Profile:        profile,
		PlanReadOnly:   a.planMode.Load() || a.turn.constraints.PlanModeReadOnly,
		Interactive:    a.hasInteractiveAsk(),
		Verification:   tool.IsShellToolName(plan.evidenceName) && evidence.IsVerificationCommand(bashCommandFromArgs(plan.evidenceArgs)),
		TestsForbidden: a.turn.constraints.ForbidTests,
		WorkspaceRoot:  a.writeWorkspaceRoot,
	})
}

func effectHintOf(t tool.Tool, args json.RawMessage) evidence.CallHint {
	if t == nil {
		return evidence.CallHint{}
	}
	hint := evidence.CallHint{Present: true, ReadOnly: t.ReadOnly()}
	if d, ok := t.(interface{ MCPDestructiveHint() bool }); ok {
		hint.Destructive = d.MCPDestructiveHint()
	}
	if p, ok := t.(tool.EffectHintProvider); ok {
		h := p.EffectHint(args)
		hint.Known = h.Known
		hint.ReadOnly = hint.ReadOnly || h.ReadOnly
		hint.Destructive = hint.Destructive || h.Destructive
		hint.Privileged = h.Privileged
		hint.UsesNetwork = h.UsesNetwork
		hint.ExecutesCode = h.ExecutesCode
		hint.Targets = append([]string(nil), h.Targets...)
	}
	return hint
}

func (a *Agent) hasInteractiveAsk() bool {
	if a == nil || a.svc.gate == nil {
		return false
	}
	_, ok := a.svc.gate.(interface {
		Ask(any) (bool, error)
	})
	return ok
}
