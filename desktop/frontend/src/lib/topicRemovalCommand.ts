import { app } from "./bridge";
import type { Translator } from "./i18n";
import type { TopicRemovalInspection, TopicRemovalRequest } from "../generated/desktopContract.generated";

// Loaded only when the user removes a legacy topic. Retries retain the original
// confirmation even when a partial write has already removed the sidebar row.
export async function removeProjectTopic(topicId: string, inspect: (id: string) => Promise<TopicRemovalInspection>, requests: Map<string, TopicRemovalRequest>, t: Translator): Promise<void | false> {
  let request = requests.get(topicId);
  if (!request) {
    const view = await inspect(topicId);
    if (!view.allowed) throw new Error(t(view.reason === "busy" ? "projectTree.sessionError.operationBusy" : "projectTree.sessionError.failed") + (view.reason && view.reason !== "busy" ? `: ${view.reason}` : ""));
    if (view.disposition !== "discard_placeholder" && !await app.ConfirmAction({
      title: t("history.moveToTrash"), message: t("history.confirmMoveToTrash"),
      detail: t("history.archiveExplanation"), confirmLabel: t("history.moveToTrash"), cancelLabel: t("common.cancel"), destructive: true,
    })) return false;
    request = { operationId: crypto.randomUUID(), target: view.target, expectedToken: view.token };
    requests.set(topicId, request);
  }
  const result = await app.RemoveTopic(request);
  if (!result.committed) {
    if (result.errorCode === "state_conflict") requests.delete(topicId);
    throw new Error(t(result.errorCode === "state_conflict" ? "projectTree.sessionError.targetChanged" : result.errorCode === "busy" ? "projectTree.sessionError.operationBusy" : "history.purgePending") + (result.errorCode === "operation_failed" && result.errorMessage ? `: ${result.errorMessage}` : ""));
  }
  requests.delete(topicId);
}
