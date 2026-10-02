import type { InboxQueueRequest, InboxQueueResult } from "./inboxQueueCommands";
import type { InboxTarget } from "./pendingFollowup";
import type { AppBindings } from "./bridge";

// Browser-only fixture using the production component and command contract.
// No live files, sessions, providers, or mutation endpoints are involved.
export function createInboxQueuePreview() {
  let revision = 1, paused = false;
  let rows = [
    { id: "queue-layout", text: "检查页面布局和交互", version: "1" },
    { id: "queue-save", text: "修复消息队列保存失败的问题。\n保留原消息的位置、附件和文件引用。\n完成后验证修改后的正文是否被正确执行。", version: "1" },
    { id: "queue-tests", text: "补充验证并整理结果", version: "1" },
  ];
  const snapshot = (path = "") => ({ revision, paused, recovered: false, sessionPath: path, mutationsSupported: true,
    items: rows.map((row, i) => ({ id: row.id, preview: row.text, state: "queued", intent: "followup", position: i + 1, byteSize: row.text.length })),
    itemsCount: rows.length, bytes: 512, maxItems: 64, maxBytes: 64 * 1024 * 1024,
  });
  const receipts = new Map<string, { itemId: string; disposition: string; position: number; paused: boolean }>();
  const enqueue = (text: string, key: string, steer = false) => {
    const previous = receipts.get(key);
    if (previous) return previous;
    const id = `queue-${++revision}`;
    if (!steer) rows.push({ id, text, version: String(revision) });
    const receipt = { itemId: id, disposition: steer ? "steer_accepted" : "queued_followup", position: steer ? 0 : rows.length, paused };
    receipts.set(key, receipt);
    return receipt;
  };
  const command = async (target: InboxTarget, request: InboxQueueRequest): Promise<InboxQueueResult> => {
    const row = rows.find(row => row.id === request.itemId);
    const result = (outcome: InboxQueueResult["outcome"], reason?: string) => ({ outcome, reason, snapshot: snapshot(target.sessionPath) });
    if (request.kind === "snapshot") return result("unchanged");
    if (request.kind === "enqueue_steer") return { ...result("applied"), receipt: enqueue(request.text || "", request.idempotencyKey || "", true) };
    if (request.kind === "pause") { paused = Boolean(request.paused); revision++; return result("applied"); }
    if (!row) return result("unavailable", "item_missing");
    if (request.kind === "read") return { ...result("unchanged"), edit: { id: row.id, text: row.text, contentVersion: row.version, references: row.id === "queue-save" ? ["问题截图.png", "Composer.tsx"] : [] } };
    if (request.kind === "edit") {
      if (row.version !== request.contentVersion) return result("conflict", "content_changed");
      row.text = request.text || ""; row.version = String(++revision);
    } else if (request.kind === "move") {
      if (revision !== request.queueRevision) return result("conflict", "order_changed");
      if (request.beforeItemId && !rows.some(row => row.id === request.beforeItemId)) return result("conflict", "anchor_missing");
      rows = rows.filter(candidate => candidate !== row);
      rows.splice(request.beforeItemId ? rows.findIndex(row => row.id === request.beforeItemId) : rows.length, 0, row); revision++;
    } else if (request.kind === "delete" || request.kind === "steer") { rows = rows.filter(candidate => candidate !== row); revision++; }
    return result("applied");
  };
  return { snapshot, command, enqueue, lookup: (key: string) => receipts.get(key) };
}

export function createInboxQueuePreviewBindings() {
  const queue = createInboxQueuePreview();
  const bindings: Pick<AppBindings, "CaptureInboxTarget" | "InboxQueueForTarget" | "EnqueueInboxFollowupForTarget" | "LookupInboxFollowupForTarget" | "InboxSnapshot"> = {
    CaptureInboxTarget: async (tabId, sessionPath) => ({ tabId, sessionPath, generation: 1, selection: 0, remote: false }),
    InboxQueueForTarget: queue.command,
    EnqueueInboxFollowupForTarget: async (_target, display, submit, _invocations, key) => queue.enqueue(submit || display, key),
    LookupInboxFollowupForTarget: async (_target, key) => {
      const receipt = queue.lookup(key);
      if (!receipt) throw new Error("Follow-up receipt unconfirmed");
      return receipt;
    },
    InboxSnapshot: async () => queue.snapshot(),
  };
  return bindings;
}
