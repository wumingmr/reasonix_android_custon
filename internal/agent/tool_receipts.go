package agent

import (
	"encoding/json"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

// recordToolReceipts files the turn-scoped evidence for one executed call:
// always the model-visible call for audit, plus the real target's attributes
// for mutation/read classification when a proxy resolved elsewhere.
func (a *Agent) finalizeObservedToolReceipts(plan *toolCallPlan, result string, execution *tool.ShellExecution, err error) evidence.Receipt {
	a.observeAfterMutation(plan)
	plan.mutationAfterDone = true
	return a.recordToolReceipts(plan, result, execution, err)
}

func (a *Agent) recordToolReceipts(plan *toolCallPlan, result string, execution *tool.ShellExecution, err error) evidence.Receipt {
	if a.task.ledger == nil {
		return evidence.Receipt{}
	}
	call := plan.call
	args := json.RawMessage(call.Arguments)
	switch {
	case plan.evidenceName != call.Name:
		proxy := evidence.ReceiptFromToolCall(call.Name, args, err == nil, true)
		proxy.ToolCallID = call.ID
		a.task.ledger.Record(proxy)
		rec := evidence.ReceiptFromToolCall(plan.evidenceName, plan.evidenceArgs, err == nil, plan.readOnly)
		rec.ToolCallID = call.ID
		rec.Mutation = plan.effects.ContentMutation
		a.stampReceiptDeliveryScope(&rec)
		decorateExecutionReceipt(&rec, result, execution)
		rec = a.task.ledger.Record(rec)
		return rec
	default:
		rec := evidence.ReceiptFromToolCall(call.Name, args, err == nil, plan.tool.ReadOnly())
		rec.ToolCallID = call.ID
		rec.Mutation = plan.effects.ContentMutation
		a.stampReceiptDeliveryScope(&rec)
		decorateExecutionReceipt(&rec, result, execution)
		rec = a.task.ledger.Record(rec)
		return rec
	}
}
