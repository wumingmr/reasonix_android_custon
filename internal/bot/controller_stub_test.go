package bot

import (
	"context"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/sandbox"
)

// stubBotController implements the complete controller port with inert
// behavior. Bot test doubles embed this value and override only the behavior
// under test. Keeping an embedded nil interface here is unsafe: a promoted
// method call becomes a real nil-pointer hardware exception, and recover does
// not make that exception harmless on every Windows runner.
type stubBotController struct{}

func (stubBotController) NewSession() error                           { return nil }
func (stubBotController) ClearSession() error                         { return nil }
func (stubBotController) Resume(*agent.Session, string)               {}
func (stubBotController) SetSessionPath(string)                       {}
func (stubBotController) SessionPath() string                         { return "" }
func (stubBotController) SessionDir() string                          { return "" }
func (stubBotController) Label() string                               { return "" }
func (stubBotController) ModelRef() string                            { return "" }
func (stubBotController) WorkspaceRoot() string                       { return "" }
func (stubBotController) Close()                                      {}
func (stubBotController) Submit(string)                               {}
func (stubBotController) SubmitDisplay(string, string)                {}
func (stubBotController) SubmitFinalReadinessRecovery(string, string) {}
func (stubBotController) SubmitDeliveryRecovery(string, string)       {}
func (stubBotController) SubmitInvocationDisplay(string, string, []control.InvocationRequest) {
}
func (stubBotController) SubmitEditedDisplay(string, string, string)              {}
func (stubBotController) SubmitHTTP(string)                                       {}
func (stubBotController) SubmitHTTPFormat(string, string)                         {}
func (stubBotController) SubmitUserTurn(string, string)                           {}
func (stubBotController) Send(string)                                             {}
func (stubBotController) SendWithRaw(string, string)                              {}
func (stubBotController) Run(context.Context, string) error                       { return nil }
func (stubBotController) RunTurn(context.Context, string) error                   { return nil }
func (stubBotController) RunFinalReadinessRecovery(context.Context, string) error { return nil }
func (stubBotController) RunShell(string)                                         {}
func (stubBotController) Cancel()                                                 {}
func (stubBotController) Steer(string)                                            {}
func (stubBotController) SteerConsumed() bool                                     { return false }
func (stubBotController) Running() bool                                           { return false }
func (stubBotController) CancelRequested() bool                                   { return false }

// Pre-seeded fake sessions represent the currently executing test turn. Mark
// them running so unrelated config defaults cannot retire the fake before the
// test drives it; tests for idle replacement override RuntimeStatus directly.
func (stubBotController) RuntimeStatus() control.RuntimeStatus {
	return control.RuntimeStatus{Running: true}
}
func (stubBotController) Turn() int                                                 { return 0 }
func (stubBotController) History() []provider.Message                               { return nil }
func (stubBotController) ToolResult(string) *control.ToolResultData                 { return nil }
func (stubBotController) Approve(string, bool, bool, bool)                          {}
func (stubBotController) ResolveApproval(string, bool, sandbox.ApprovalScope) error { return nil }
func (stubBotController) ResolvePlanDecision(string, control.PlanDecisionAction) error {
	return nil
}
func (stubBotController) ResolvePlanDecisionWithFeedback(string, control.PlanDecisionAction, string) error {
	return nil
}
func (stubBotController) ResolveRecovery(string, agent.RecoveryAction, string) error { return nil }
func (stubBotController) AnswerMCPInteraction(string, string, map[string]any)        {}
func (stubBotController) AnswerQuestion(string, []event.AskAnswer)                   {}
func (stubBotController) AnswerQuestionChecked(string, []event.AskAnswer) error      { return nil }
func (stubBotController) AnswerMCPInteractionChecked(string, string, map[string]any) error {
	return nil
}
func (stubBotController) Ask(context.Context, []event.AskQuestion) ([]event.AskAnswer, error) {
	return nil, nil
}
func (stubBotController) ReplayPendingPrompts()                      {}
func (stubBotController) ReplayPendingPromptsTo(event.Sink)          {}
func (stubBotController) ReplayPendingPromptsWith(func() event.Sink) {}
func (stubBotController) PendingPrompt() bool                        { return false }
func (stubBotController) EnableInteractiveApproval()                 {}
func (stubBotController) ToolApprovalMode() string                   { return "" }
func (stubBotController) SetToolApprovalMode(string)                 {}
func (stubBotController) AutoApproveTools() bool                     { return false }
func (stubBotController) SetAutoApproveTools(bool)                   {}
func (stubBotController) Bypass() bool                               { return false }
func (stubBotController) SetBypass(bool)                             {}
func (stubBotController) SetMode(bool, bool)                         {}

var _ botController = stubBotController{}
