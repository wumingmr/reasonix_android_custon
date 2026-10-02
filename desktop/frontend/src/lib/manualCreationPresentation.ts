import type { ManualSessionCreationView } from "../generated/desktopContract.generated";
import type { DictKey } from "./i18n";

export function manualCreationPresentation(item: ManualSessionCreationView): { key: DictKey; action?: "retry" | "new" | "project" } {
  const status = item.progress?.status;
  // Host recovery supersedes an older durable error. Internal stages are not
  // user decisions; expose only a meaningful wait or one next action.
  if (status === "waiting_workspace") return { key: "creation.workspaceUnavailable" };
  if (status && ["waiting_lock", "retrying_storage", "stopping", "running", "queued"].includes(status)) {
    return { key: item.progress?.slow ? "creation.slow" : "creation.preparing" };
  }
  const code = /session_operation:([^:]+):/.exec(item.error || "")?.[1] || item.progress?.errorCode;
  if (code === "workspace_removed" || code === "workspace_changed") return { key: "creation.workspaceChanged", action: "project" };
  if (code === "creation_cancelled" || code === "creation_owner_conflict") return { key: "creation.unavailable", action: "new" };
  if (status === "blocked" || status === "stopped" || item.phase === "failed") return { key: "creation.failed", action: "retry" };
  return { key: "creation.preparing" };
}
