package runtimepolicy

import (
	"encoding/json"

	"reasonix/internal/evidence"
)

// GuardAction is one monotonic preflight verdict.
type GuardAction uint8

const (
	GuardAbstain GuardAction = iota
	GuardAllow
	GuardAsk
	GuardDeny
)

// GuardDecision is one guard's immutable snapshot.
type GuardDecision struct {
	Action  GuardAction
	Reasons []string
	Message string
}

// CallContext is the resolved, already-identified tool call.
type CallContext struct {
	ToolName             string
	Args                 json.RawMessage
	Profile              evidence.EffectProfile
	PlanReadOnly         bool
	Interactive          bool
	Verification         bool
	TestsForbidden       bool
	WorkspaceRoot        string
	PriorWriteTargets    []evidence.TargetKey
	PriorProductionWrite bool
}

// ResultContext is the frozen post-execute receipt.
type ResultContext struct {
	Seq            int
	Receipt        evidence.Receipt
	Profile        evidence.EffectProfile
	WorkspaceRoot  string
	TestsForbidden bool
}

// Guard is one monotonic pipeline stage.
type Guard interface {
	BeforeTool(CallContext) GuardDecision
	AfterTool(ResultContext) []evidence.Receipt
}

// MergeDecisions applies Deny > Ask > Allow > Abstain and concatenates
// obligations. Later guards cannot revoke a stronger action.
func MergeDecisions(decisions ...GuardDecision) GuardDecision {
	out := GuardDecision{Action: GuardAbstain}
	for _, d := range decisions {
		if d.Action > out.Action {
			out.Action = d.Action
			if d.Message != "" {
				out.Message = d.Message
			}
		} else if out.Message == "" && d.Message != "" && d.Action == out.Action {
			out.Message = d.Message
		}
		out.Reasons = append(out.Reasons, d.Reasons...)
	}
	return out
}
