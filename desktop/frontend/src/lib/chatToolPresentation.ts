import type { Item } from "./useController";
import { isShellToolName, isPowerShellToolName } from "./shellToolIdentity";

export type ToolItem = Extract<Item, { kind: "tool" }>;
export type ToolPresentationKind = "search" | "web" | "shell" | "agent" | "file" | "present" | "tool";

/** Shell execution metadata is authoritative for shell calls, not output readers. */
export function toolPresentation(item: ToolItem): {
  state: ToolItem["status"]; dot: "ongoing" | "done" | "error" | "warning" | "idle";
  label: "chat.running" | "chat.done" | "chat.failed" | "chat.stopped" | "chat.timedOut" | "chat.notRun" | "chat.background" | "chat.unknown";
  exitCode?: number;
} {
  const execution = classifyTool(item) === "shell" ? item.execution : undefined;
  switch (execution?.state) {
    case "timed_out": return { state: "error", dot: "error", label: "chat.timedOut" };
    case "cancelled": return { state: "stopped", dot: "warning", label: "chat.stopped" };
    case "not_run": return { state: "stopped", dot: "warning", label: "chat.notRun" };
    // This is the settled launch receipt, not a live observation of the job.
    // Job progress is reported by its own output/status tools across turns.
    case "background_started": return { state: "done", dot: "done", label: "chat.background" };
    case "failed": return { state: "error", dot: "error", label: "chat.failed", exitCode: execution.exitCode };
    case "completed":
      if (execution.exitCode == null) return { state: "unknown", dot: "idle", label: "chat.unknown" };
      return execution.exitCode === 0 ? { state: "done", dot: "done", label: "chat.done", exitCode: 0 }
        : { state: "error", dot: "error", label: "chat.failed", exitCode: execution.exitCode };
  }
  if (item.status === "running") return { state: "running", dot: "ongoing", label: "chat.running" };
  if (item.resultMissing || item.status === "unknown") return { state: "unknown", dot: "idle", label: "chat.unknown" };
  if (item.status === "error" || item.error || (execution?.exitCode != null && execution.exitCode !== 0)) {
    return { state: "error", dot: "error", label: "chat.failed", exitCode: execution?.exitCode };
  }
  if (item.status === "stopped") return { state: "stopped", dot: "warning", label: "chat.stopped" };
  if (classifyTool(item) === "shell" && (execution?.exitCode == null || (execution.state && execution.state !== "running"))) {
    return { state: "unknown", dot: "idle", label: "chat.unknown" };
  }
  return { state: "done", dot: "done", label: "chat.done", exitCode: execution?.exitCode };
}

const AGENT_TOOLS = new Set(["task", "read_only_task", "parallel_tasks", "fleet", "subagent"]);
const FILE_TOOLS = new Set([
  "read_file", "glob", "grep", "ls", "code_index",
  "write_file", "edit_file", "multi_edit", "move_file", "notebook_edit", "delete_range", "delete_symbol",
]);

/**
 * Resolve a renderer from trusted built-in identities. A plugin whose display
 * name happens to contain "read" or "write" must remain a generic tool.
 */
export function classifyTool(item: ToolItem): ToolPresentationKind {
  if (item.name === "present") return "present";
  if (item.name === "web_search") return "search";
  if (item.name === "web_fetch") return "web";
  if (isShellToolName(item.name) || item.isShell) return "shell";
  if (AGENT_TOOLS.has(item.name)) return "agent";
  if (FILE_TOOLS.has(item.name)) return "file";
  return "tool";
}

export function shellDisplayName(item: ToolItem): string {
  const shell = item.execution?.shell?.trim().toLowerCase() || (isPowerShellToolName(item.name) ? "pwsh" : "");
  if (shell === "powershell" || shell === "pwsh") return "PowerShell";
  if (shell === "git-bash") return "Git Bash";
  if (shell === "bash") return "Bash";
  if (shell === "zsh") return "Zsh";
  if (shell === "sh") return "Shell";
  return "Terminal";
}
