package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

// CompleteSubtaskTool optionally records the child's own completion report.
// It is never registered on the parent agent's tool surface.
type CompleteSubtaskTool struct{}

func NewCompleteSubtaskTool() *CompleteSubtaskTool { return &CompleteSubtaskTool{} }

func (*CompleteSubtaskTool) Name() string { return tool.HostCompleteSubtask }

func (*CompleteSubtaskTool) Description() string {
	return "Optionally report this delegated sub-task's outcome to the parent. status is complete, partial, blocked, or failed; summary states what is now true; acceptance_criteria lists your assessment and supporting information; unresolved lists what remains. This is your report; the host displays actual execution facts separately. A final prose answer is also sufficient."
}

// ReadOnly is true: submitting a report changes no workspace state. It is the
// claim itself, not a mutation.
func (*CompleteSubtaskTool) ReadOnly() bool { return true }

func (*CompleteSubtaskTool) Schema() json.RawMessage {
	// Fixed schema — sub-agent registries only, so it never enters the parent
	// prefix.
	return json.RawMessage(`{
"type":"object",
"properties":{
  "status":{"type":"string","description":"Your assessment: complete | partial | blocked | failed."},
  "summary":{"type":"string","description":"What is now true as a result of this sub-task."},
  "acceptance_criteria":{
    "type":"array",
    "description":"Your assessment of each condition and its supporting information.",
    "items":{
      "type":"object",
      "properties":{
        "id":{"type":"string","description":"Short stable id, e.g. AC1."},
        "status":{"type":"string","description":"satisfied | unsatisfied"},
        "evidence":{
          "type":"array",
          "items":{
            "type":"object",
            "properties":{
              "kind":{"type":"string","enum":["verification","review","diff","files","manual"],"description":"verification = a command was run (command REQUIRED); review = a review completed; diff = a code change (paths REQUIRED); files = files created/edited/inspected (paths REQUIRED); manual = a manual check, which the host cannot back on its own."},
              "summary":{"type":"string","description":"The evidence itself."},
              "command":{"type":"string","description":"REQUIRED for verification: the command as it actually ran."},
              "paths":{"type":"array","items":{"type":"string"},"description":"REQUIRED for diff/files: the files this evidence refers to."}
            },
            "required":["kind","summary"]
          }
        }
      },
      "required":["id","status"]
    }
  },
  "unresolved":{"type":"array","items":{"type":"string"},"description":"What remains undone, unverified, or blocked. Put anything you assumed rather than checked here."}
},
"required":["status","summary"]
}`)
}

func (*CompleteSubtaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	report, err := evidence.ParseCompletionReport(args)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("complete_subtask recorded as model report: status=%s criteria=%d unresolved=%d",
		report.Status, len(report.Criteria), len(report.Unresolved)), nil
}

// AttachCompleteSubtaskTool adds complete_subtask to a sub-agent registry.
// Parent registries never receive it.
func AttachCompleteSubtaskTool(reg *tool.Registry) {
	if reg == nil {
		return
	}
	reg.Add(NewCompleteSubtaskTool())
}

// CompletionReport returns the model's report without host adjudication.
func (a *Agent) CompletionReport() (evidence.CompletionReport, bool) {
	if a == nil || a.task.ledger == nil {
		return evidence.CompletionReport{}, false
	}
	return a.task.ledger.LatestCompletionReport()
}

// completeSubtaskContract is appended to a sub-agent's task prompt when the
// host offers a typed completion report as an alternative to prose.
const completeSubtaskContract = `<completion-contract>
Finish with a clear final answer, or optionally use complete_subtask to provide a
structured report. State what you completed and what remains uncertain or undone.
Your completion assessment is shown separately from host-recorded execution facts.
</completion-contract>`
