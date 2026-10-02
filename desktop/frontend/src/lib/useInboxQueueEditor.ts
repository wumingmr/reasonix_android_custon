import { useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { app } from "./bridge";
import { inboxQueueDrafts, type QueueEditDraft } from "./inboxQueueDrafts";
import type { InboxQueueRequest, InboxQueueResult } from "./inboxQueueCommands";
import type { InboxSnapshotLike } from "./composerInboxQueue";
import type { InboxTarget } from "./pendingFollowup";

export function useInboxQueueEditor(scope: string, tabId: string, sessionPath: string, apply: (snapshot: InboxSnapshotLike) => unknown, refresh: () => void) {
  const drafts = useSyncExternalStore(inboxQueueDrafts.subscribe, () => inboxQueueDrafts.get(scope));
  const [open, setOpen] = useState(Boolean(drafts.edit));
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const owner = useRef<object | null>(null);
  const gate = useRef<{ owner: object } | null>(null);
  useLayoutEffect(() => {
    owner.current = {};
    gate.current = null;
    setBusy(false);
    return () => { owner.current = null; gate.current = null; };
  }, [scope, tabId, sessionPath]);
  // Component lifetime, not just session path: leaving and returning to the
  // same session creates a new editor that an old reply must never clear.
  const operation = async (run: (check: () => void) => Promise<void>) => {
    if (gate.current) return;
    if (!owner.current) return;
    const ticket = { owner: owner.current };
    gate.current = ticket; setBusy(true);
    const current = () => owner.current === ticket.owner && gate.current === ticket;
    const check = () => { if (!current()) throw new Error("queue editor operation expired"); };
    try { await run(check); } catch { if (current()) setNotice("error"); }
    finally { if (current()) { gate.current = null; setBusy(false); } }
  };
  const capture = async (check: () => void) => {
    check();
    if (!app.CaptureInboxTarget || !app.InboxQueueForTarget) throw new Error("unsupported");
    const target = await app.CaptureInboxTarget(tabId, sessionPath);
    check();
    return target;
  };
  const call = async (target: InboxTarget, request: InboxQueueRequest, check: () => void): Promise<InboxQueueResult> => {
    check();
    if (!app.InboxQueueForTarget) throw new Error("unsupported");
    const result = await app.InboxQueueForTarget(target, request);
    check();
    if (result.snapshot?.sessionPath === sessionPath) apply(result.snapshot);
    return result;
  };
  const edit = (id: string) => operation(async check => {
    const retained = inboxQueueDrafts.get(scope).edit;
    if (retained?.id === id) { setOpen(true); return; }
    const target = await capture(check);
    const result = await call(target, { kind: "read", itemId: id }, check);
    if (!result.edit) { setNotice(result.reason || "error"); return; }
    const state = inboxQueueDrafts.get(scope);
    if (state.edit !== retained) return;
    inboxQueueDrafts.set(scope, { edit: { ...result.edit, target, value: result.edit.text }, recovered: state.edit && (state.edit.value !== state.edit.text || state.edit.reason) ? [...state.recovered, state.edit] : state.recovered });
    setNotice(""); setOpen(true);
  });
  const update = (value: string) => {
    const state = inboxQueueDrafts.get(scope);
    if (state.edit) inboxQueueDrafts.set(scope, { ...state, edit: { ...state.edit, value } });
  };
  const save = () => operation(async check => {
    const state = inboxQueueDrafts.get(scope), draft = state.edit;
    if (!draft || !draft.value.trim()) return;
    let result: InboxQueueResult;
    try {
      result = await call(draft.target, { kind: "edit", itemId: draft.id, text: draft.value, contentVersion: draft.contentVersion }, check);
    } catch {
      check();
      // Reconcile without resubmitting. A missing/executing item is not proof
      // that the attempted save succeeded.
      try { await call(draft.target, { kind: "snapshot" }, check); } catch { /* Keep draft. */ }
      check();
      if (inboxQueueDrafts.get(scope).edit !== draft) return;
      inboxQueueDrafts.set(scope, { ...inboxQueueDrafts.get(scope), edit: { ...draft, reason: "unconfirmed" } });
      setNotice("unconfirmed"); return;
    }
    if (inboxQueueDrafts.get(scope).edit !== draft) return;
    if (result.outcome === "applied" || result.outcome === "unchanged") {
      inboxQueueDrafts.set(scope, { ...inboxQueueDrafts.get(scope), edit: undefined });
      setOpen(false); setNotice("saved");
    } else {
      inboxQueueDrafts.set(scope, { ...inboxQueueDrafts.get(scope), edit: { ...draft, reason: result.reason || "error" } });
      setNotice(result.reason || "error");
    }
    refresh();
  });
  const mutate = (request: InboxQueueRequest) => operation(async check => {
    const result = await call(await capture(check), request, check);
    setNotice(result.reason || ""); refresh();
  });
  const recover = (): string | undefined => {
    if (gate.current) return;
    const state = inboxQueueDrafts.get(scope);
    if (!state.edit) return;
    inboxQueueDrafts.set(scope, { edit: undefined, recovered: [...state.recovered, state.edit] });
    setOpen(false); setNotice("retained");
    return state.edit.value;
  };
  const reopen = (draft: QueueEditDraft) => {
    if (gate.current) return;
    const state = inboxQueueDrafts.get(scope);
    inboxQueueDrafts.set(scope, { edit: draft, recovered: [...state.recovered.filter(d => d !== draft), ...(state.edit ? [state.edit] : [])] });
    setOpen(true);
  };
  const reload = () => operation(async check => {
    const state = inboxQueueDrafts.get(scope);
    if (!state.edit) return;
    // Reload is an explicit rebase onto the current session selection. Saving
    // still uses the originally captured target and content version.
    const target = await capture(check);
    const result = await call(target, { kind: "read", itemId: state.edit.id }, check);
    if (!result.edit) { setNotice(result.reason || "error"); return; }
    const latest = inboxQueueDrafts.get(scope);
    if (latest.edit !== state.edit) return;
    inboxQueueDrafts.set(scope, { recovered: [...latest.recovered, state.edit], edit: { ...result.edit, target, value: result.edit.text } });
    setNotice("");
  });
  const close = () => {
    if (gate.current) return;
    const state = inboxQueueDrafts.get(scope);
    if (state.edit && state.edit.value === state.edit.text && !state.edit.reason) {
      inboxQueueDrafts.set(scope, { ...state, edit: undefined });
    }
    setOpen(false); setNotice("");
  };
  return { drafts, open, busy, notice, edit, update, save, mutate, recover, reopen, reload, close, show: () => { if (!gate.current) setOpen(true); } };
}
