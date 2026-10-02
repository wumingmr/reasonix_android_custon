import type { HistoryMessage, HistoryToolCall } from "./types";
import type { ToolStatus } from "./useController";
import { isShellToolName } from "./shellToolIdentity";

/** Missing resident payload is not evidence that execution stopped. */
export function historyToolStatus(result: HistoryMessage | undefined, call?: HistoryToolCall, error?: string): ToolStatus {
  const name = call?.name || result?.toolName;
  const shell = !name || isShellToolName(name) || (call?.id || result?.toolCallId || "").startsWith("shell-");
  // A job-output read can carry a snapshot of a still-running (or failed)
  // process. That process is not the invocation whose history we are restoring.
  const evidence = (shell ? result?.execution?.state : undefined) ?? call?.resultObservation?.state;
  if (evidence === "cancelled" || evidence === "not_started") return "stopped";
  if (evidence === "failed" || evidence === "error") return "error";
  if (error) return "error";
  if (evidence === "completed" || evidence === "user_confirmed") return "done";
  if (evidence === "running" || evidence === "started" || evidence === "pending" || call?.pending) return "running";
  return result ? "done" : "unknown";
}
