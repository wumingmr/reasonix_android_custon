package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

// parseToolCall resolves the canonical tool and rejects ambiguity/unknown tools.
func (a *Agent) parseToolCall(ctx context.Context, turn *turnRuntime, plan *toolCallPlan) (toolOutcome, bool) {
	if retiredTool(plan.call.Name) {
		msg := fmt.Sprintf("tool_retired: %s is no longer part of the execution protocol; use file tools, todo_write, or provide the final answer directly", plan.call.Name)
		return toolOutcome{output: msg, errMsg: "tool_retired"}, true
	}
	t, canonicalName, ambiguous := a.svc.tools.ResolveCall(plan.call.Name)
	if len(ambiguous) > 0 {
		msg := fmt.Sprintf("ambiguous MCP tool reference %q; use one of: %s", plan.call.Name, strings.Join(ambiguous, ", "))
		return toolOutcome{
			output: "error: " + msg,
			errMsg: msg,
		}, true
	}
	if t == nil {
		if server, ok := completedMCPConnect(a.svc.tools, plan.call.Name); ok {
			return toolOutcome{
				output: fmt.Sprintf("MCP server %q is connected; its real tools are now available", server),
			}, true
		}
		return toolOutcome{
			output: fmt.Sprintf("error: unknown tool %q", plan.call.Name),
			errMsg: fmt.Sprintf("unknown tool %q", plan.call.Name),
		}, true
	}
	plan.tool = t
	plan.canonicalName = canonicalName
	plan.permName = canonicalName
	plan.permArgs = json.RawMessage(plan.call.Arguments)
	plan.execTool = t
	plan.execArgs = json.RawMessage(plan.call.Arguments)
	plan.evidenceName = canonicalName
	plan.evidenceArgs = json.RawMessage(plan.call.Arguments)
	plan.readOnly = t.ReadOnly()
	if canonicalName == "read_file" {
		if out, blocked := a.resolveReadCursor(plan); blocked {
			return out, true
		}
	}
	if tool.IsShellToolName(canonicalName) {
		var permissionReader bool
		plan.effects, permissionReader = evidence.ClassifyBashToolCall(plan.execArgs)
		if permissionReader {
			// Carry the resolved read-only classification without changing the schema.
			plan.readOnly = true
			plan.resolvedMeta = &tool.ResolvedCall{TargetName: canonicalName, ReadOnly: true}
		}
	} else {
		plan.effects = evidence.ClassifyToolCall(plan.evidenceName, plan.evidenceArgs, plan.readOnly)
	}
	return toolOutcome{}, false
}

func retiredTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "complete_step", "review_report", "read_policy_receipt", "session_read_strategy_receipt":
		return true
	default:
		return false
	}
}
