package agent

import (
	"errors"
	"fmt"

	"reasonix/internal/workspacelease"
)

func workspaceWriteBlockedOutcome(plan *toolCallPlan, err error) toolOutcome {
	out := toolOutcome{blocked: true, errMsg: "blocked: workspace write lease unavailable"}
	var conflict *workspacelease.ConflictError
	if !errors.As(err, &conflict) {
		out.output = fmt.Sprintf("blocked: write coordination failed before tool execution: %v. No hooks or tool execution started; fix the error before retrying.", err)
		return out
	}
	out.output = fmt.Sprintf("blocked: WRITE_CONFLICT: %v. This call was NOT executed: no tool hooks, checkpoints, or file changes started. No automatic wait or retry was queued. ", conflict)
	switch conflict.Scope {
	case "file":
		out.output += "Choose/create a different file and continue independent work. Do not overwrite the contested file through bash or another tool. If this exact file is required, re-read its current contents after the other write finishes and reconcile the changes before retrying."
	case "session":
		out.output += "This session's own write coordination is busy (possibly a background/subagent write); this is not proof that another session owns the requested file. Continue read-only work, or finish/cancel the relevant own job before retrying."
	default:
		out.output += "A workspace-wide/ancestor guard or queue is busy; this does NOT prove that the requested file itself is being edited. "
		if plan.hooksMayMutateWorkspace {
			out.output += "Configured tool hooks can write arbitrary files, so even a path-specific or read-only tool needs workspace protection. Changing only the tool's path cannot bypass those hooks; continue independent work without mutating hooks or retry after the writer finishes."
		} else if pathBoundWriterNames[plan.runTool.Name()] {
			out.output += "Another opaque command or guard may currently cover this directory. Continue read-only work or write outside that protected workspace; re-read this file before a later retry."
		} else {
			out.output += "This command/tool has unproven write targets (scripts, builds, git mutations, MCP); additional_write_dirs is permission, NOT a file-level claim. For file changes, split the operation into read_file plus edit_file/write_file/move_file with explicit, non-conflicting paths. Pure read-only commands may continue. Do not blindly retry the same opaque command in a loop."
		}
	}
	return out
}
