import type { Action, Item, State } from "./useController";
import type { WireEvent } from "./types";
import { acceptSessionRuntimeSnapshot, type RuntimeState } from "./runtimeStateStore";
import { interruptOrphanedSessionOperationItems, sessionOperationItem, upsertSessionOperationItem } from "./sessionMaintenanceOperation";

function lastPendingCompaction(items: readonly Item[], manual = false): number {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind === "compaction" && item.pending && (!manual || item.operationId)) return i;
  }
  return -1;
}

export function reduceCompactionEvent(s: State, e: WireEvent): State {
  switch (e.kind) {
    case "session_operation": {
      const op = e.sessionOperation;
      if (!op?.operationId) return s;
      const updated = upsertSessionOperationItem(s.items, { ...sessionOperationItem(op), observedRuntimeRevision: s.runtimeStateSnapshot?.revision ?? 0 });
      return { ...s, seq: s.seq + (updated.inserted ? 1 : 0), items: updated.items };
    }
    case "compaction_started":
      if (lastPendingCompaction(s.items, true) >= 0) return s;
      return { ...s, seq: s.seq + 1, items: [...s.items, { kind: "compaction", id: `c${s.seq}`, pending: true, trigger: e.compaction?.trigger ?? "", messages: 0, summary: "", archive: "" }] };
    case "compaction_done": {
      const c = e.compaction;
      const operationAt = lastPendingCompaction(s.items, true);
      if (operationAt >= 0) {
        const items = s.items.map((it, i) => i === operationAt && it.kind === "compaction" ? { ...it,
          messages: c?.messages ?? it.messages, summary: c?.summary ?? it.summary, archive: c?.archive ?? it.archive } : it);
        return { ...s, items };
      }
      const at = lastPendingCompaction(s.items);
      if (!c?.summary) {
        const items = at < 0 ? s.items : s.items.filter((_, i) => i !== at);
        return { ...s, running: s.turnActive ? s.running : false, items };
      }
      const filled: Item = { kind: "compaction", id: at < 0 ? `c${s.seq}` : (s.items[at] as Extract<Item, { kind: "compaction" }>).id, pending: false, trigger: c.trigger ?? "", messages: c.messages ?? 0, summary: c.summary, archive: c.archive ?? "" };
      const items = at < 0 ? [...s.items, filled] : s.items.map((it, i) => (i === at ? filled : it));
      return { ...s, running: s.turnActive ? s.running : false, seq: s.seq + 1, items };
    }
    default: return s;
  }
}

export function reduceMaintenanceRuntimeSnapshot(s: State, snapshot: RuntimeState): State {
  const runtimeStateSnapshot = acceptSessionRuntimeSnapshot(s.runtimeStateSnapshot, snapshot);
  if (runtimeStateSnapshot === s.runtimeStateSnapshot) return s;
  const meta = runtimeStateSnapshot.todos !== undefined && s.meta
    ? { ...s.meta, runtimeStateSnapshot, canonicalTodos: runtimeStateSnapshot.todos }
    : s.meta;
  const maintenance = runtimeStateSnapshot.maintenance;
  if (!maintenance) return { ...s, meta, runtimeStateSnapshot };
  const status = ["cancelling", "finalizing", "recovery_required"].includes(maintenance.activity)
    ? maintenance.activity : "running";
  const updated = upsertSessionOperationItem(s.items, { ...sessionOperationItem({
    ...maintenance,
    status: maintenance.status || status,
    runtimeEpoch: maintenance.runtimeEpoch || runtimeStateSnapshot.runtimeEpoch,
  }), observedRuntimeRevision: runtimeStateSnapshot.revision });
  return { ...s, meta, runtimeStateSnapshot, items: updated.items, seq: s.seq + (updated.inserted ? 1 : 0) };
}

export function reconcileMaintenanceState(next: State, a: Action): State {
  const historyOrRuntimeSynchronized = ["runtime_snapshot", "meta", "history", "history_page",
    "history_replace", "history_rebase", "history_prepend", "history_append", "history_items_patch",
    "transcript_snapshot", "transcript_v2_snapshot", "transcript_page", "transcript_records"].includes(a.type);
  if (historyOrRuntimeSynchronized && next.runtimeStateSnapshot) {
    const maintenance = next.runtimeStateSnapshot.maintenance;
    const items = interruptOrphanedSessionOperationItems(
      next.items,
      maintenance?.operationId,
      maintenance?.runtimeEpoch || next.runtimeStateSnapshot.runtimeEpoch,
      next.runtimeStateSnapshot.revision,
    );
    if (items !== next.items) next = { ...next, items };
  }
  return next;
}
