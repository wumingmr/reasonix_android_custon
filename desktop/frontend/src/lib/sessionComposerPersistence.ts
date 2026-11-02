import { useEffect, useMemo, useSyncExternalStore } from "react";
import { app, sessionPurgeHandlers } from "./bridge";
import type { SessionRef } from "./sessionRef";
import type { PersistentComposerDraft, PersistentComposerTarget } from "./composerDraftTypes";
import type { SessionComposerState, SessionLifecycleResult } from "../generated/desktopContract.generated";
import { followupNotSubmitted } from "./pendingFollowup";
import { submissionOutcome, modelApplicationError, type ModelApplicationDetails } from "./modelApplication";

export const emptySessionInput = (): PersistentComposerDraft => ({ text: "", invocations: [], attachments: [], workspaceRefs: [], pastedBlocks: [], openPastedLabels: [], sessionRefs: [], selectedTextRefs: [] });
type Entry = { ref: SessionRef; state?: SessionComposerState; content: PersistentComposerDraft; generation: number; version: number; saved: number; users: number; touched: number;
  retired?: boolean; retirement: Promise<void>; retire: () => void;
  inputTasks: Set<Promise<unknown>>;
  application?: ModelApplicationDetails; error?: string; taskError?: string; unreadable?: boolean; registering?: boolean; recovering?: Promise<void>; unsubmittedId?: string; settledId?: string; tasks: Set<Promise<unknown>>; loading?: Promise<void>; saving?: Promise<void>; timer?: ReturnType<typeof setTimeout> };
const entries = new Map<string, Entry>();
const tabs = new Map<string, string>();
const tabOwners = new Map<string, symbol>();

export function sendPersistedComposer<T>(tabId: string, display: string, submit: string, id: string, send: () => T | Promise<T>, expectedEdit?: number, sourceKey = tabs.get(tabId), kind = "turn") {
 const entry = entries.get(sourceKey || "");
 const owner = tabOwners.get(tabId);
 const task = (async () => {
  const complete = await beginPersistedComposerSubmission(tabId,id,JSON.stringify({kind,display,submit}),expectedEdit,sourceKey,owner);
  try {
    if (tabs.get(tabId) !== sourceKey || tabOwners.get(tabId) !== owner) throw new Error("reasonix_error:inbox_not_submitted");
    const result = await send();
    await complete?.("accepted");
    return result;
  } catch (error) {
    const message=String(error);
    const outcome = submissionOutcome(error);
    const rejected = outcome ? outcome === "not_accepted" : followupNotSubmitted(error) || /^(?:Error:\s*)?submission not accepted(?:\n|$)/.test(message);
    await complete?.(rejected ? "not_accepted" : "unknown");
    throw error;
  }
 })().catch(async error => {
   if (entry) await recoverInput(entry).catch(() => {});
   throw error;
 });
 if (entry) { entry.tasks.add(task); void task.finally(()=>{entry.tasks.delete(task);prune();}).catch(()=>{}); }
 return task;
}
const listeners = new Set<() => void>();
let serial = 0, revision = 0, exiting = false;
const keyFor = (ref: SessionRef) => `${ref.hostId}:${ref.sessionId}`;
const notify = () => { revision++; listeners.forEach(fn => fn()); };
const subscribe = (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn); }; };
const snapshot = () => revision;
// Only successful purge receipts revoke draft ownership. A missing-session
// read/save error is not evidence that the user authorized discarding input.
export function reconcileSessionComposerPurge(method: string, args: unknown[], result: unknown) {
  const refs = method === "PurgeCanonicalSession" ? [args[0] as SessionRef]
    : (args[0] as { action: string }).action === "purge"
      ? (result as SessionLifecycleResult).items.filter(item => item.committed).map(item => item.ref ?? item.target.ref) : [];
  for (const ref of refs) {
    if (!ref) continue;
    const key = keyFor(ref), entry = entries.get(key);
    if (!entry) continue;
    entry.retired = true; entry.retire();
    if (entry.timer) clearTimeout(entry.timer);
    entries.delete(key);
    for (const [tab, mapped] of tabs) if (mapped === key) { tabs.delete(tab); tabOwners.delete(tab); }
  }
  notify();
}
// Register before any composer owner can exist, including while an RPC is pending.
for (const method of ["ApplySessionLifecycle", "PurgeCanonicalSession"]) {
  sessionPurgeHandlers[method] = (args, result) => reconcileSessionComposerPurge(method, args, result);
}
function prune() {
  const idle=[...entries.entries()].filter(([,entry])=>entry.users===0 && entry.state && !entry.error && !entry.recovering && !entry.loading && !entry.saving && !entry.tasks.size && entry.version===entry.saved).sort((a,b)=>b[1].touched-a[1].touched);
  for (const [key,entry] of idle.slice(20)) {
    if (entry.timer) clearTimeout(entry.timer);
    entries.delete(key);
    for (const [tab,mapped] of tabs) if(mapped===key) tabs.delete(tab);
  }
}
function parse(raw: string): PersistentComposerDraft {
  const value = JSON.parse(raw || "{}");
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid saved input");
  if (value.text !== undefined && typeof value.text !== "string") throw new Error("Invalid saved input text");
  for (const field of ["invocations","attachments","workspaceRefs","pastedBlocks","openPastedLabels","sessionRefs","selectedTextRefs"]) {
    if (value[field] !== undefined && !Array.isArray(value[field])) throw new Error(`Invalid saved input ${field}`);
  }
  const content = { ...emptySessionInput(), ...value } as PersistentComposerDraft;
  // Preview URLs are temporary renderer resources and never durable data.
  content.attachments = (content.attachments || []).map(attachment => ({ ...attachment, previewUrl: undefined, file: undefined }));
  return content;
}
function encoded(content: PersistentComposerDraft) {
  return JSON.stringify({ ...content, attachments: content.attachments.map(attachment => ({ ...attachment,
    path: attachment.recoveryPath || attachment.path, draftId: attachment.recoveryPath ? undefined : attachment.draftId,
    previewUrl: undefined, file: undefined })) });
}
function entryFor(ref: SessionRef) {
  const key = keyFor(ref);
  let entry = entries.get(key);
  if (!entry) {
    let retire!: () => void;
    const retirement = new Promise<void>(resolve => { retire = resolve; });
    entry = { ref, content: emptySessionInput(), generation: ++serial, version: 0, saved: 0, users:0, touched:Date.now(), tasks: new Set(), inputTasks: new Set(), retirement, retire }; entries.set(key, entry);
  }
  return entry;
}
const submissionLocked = (entry: Entry) => entry.retired || !entry.state || Boolean(entry.registering || entry.unsubmittedId || entry.state.submissionId);
const locked = (entry: Entry) => submissionLocked(entry) || Boolean(entry.recovering || entry.unreadable);
async function load(entry: Entry) {
  if (entry.loading) return entry.loading;
  entry.loading = (async () => {
    try {
      const state = await app.GetSessionComposerState(entry.ref);
      if (entry.retired || entry.version !== entry.saved) return;
      const content=parse(state.contentJson);
      entry.state = state; entry.content = content; entry.error = undefined; entry.unreadable = false;
      // Reading durable input must not require a live attachment controller.
      // Composer restores and validates external references before submission.
      const attachments = entry.content.attachments;
      void Promise.all(attachments.map(async attachment => {
        try {
          const previewUrl = await app.AttachmentDataURLForComposerTarget({kind:"session",session:entry.ref},attachment.recoveryPath || attachment.path);
          if (entries.get(keyFor(entry.ref))!==entry || !entry.content.attachments.includes(attachment)) return;
          entry.content = {...entry.content,attachments:entry.content.attachments.map(item=>item===attachment ? {...item,previewUrl} : item)};
          notify();
        } catch { /* Missing files remain visible as repairable references. */ }
      }));
    } catch (error) { if (!entry.retired) { entry.error = String(error); entry.unreadable = true; } }
    finally { entry.loading = undefined; notify(); prune(); }
  })();
  return entry.loading;
}
async function flush(entry: Entry) {
  if (entry.retired) return;
  if (entry.recovering) await entry.recovering;
  if (entry.loading) await entry.loading;
  if (entry.retired) return;
  if (entry.saving) { await entry.saving; if (entry.version > entry.saved) return flush(entry); return; }
  if (entry.error || !entry.state) throw new Error(entry.error || "Input is not loaded");
  if (entry.version === entry.saved && !entry.state.historyChanged) return;
  entry.saving = (async () => {
    let rebased = false;
    while (!entry.retired && (entry.saved < entry.version || entry.state?.historyChanged)) {
      if (!entry.state || entry.state.submissionId) throw new Error("Check the previous submission before editing");
      const version = entry.version;
      const result = await app.SaveSessionComposerState({ ref: entry.ref, expectedRevision: entry.state.revision,
        contentJson: encoded(entry.content), contentVersion: 1, acknowledgeHistory: true });
      if (entry.retired) return;
      if (result.conflict) {
        entry.state = result;
        if (rebased || result.submissionId) throw new Error("Input changed during saving; retry to save the retained input");
        // Rebase once without replacing the input currently being edited.
        rebased = true; continue;
      }
      if (result.historyChanged) { entry.state = result; throw new Error("The conversation changed. Review the saved input."); }
      entry.state = result; entry.saved = version;
    }
  })();
  try { await entry.saving; } catch (error) { if (!entry.retired) { entry.error = String(error); throw error; } }
  finally { entry.saving = undefined; notify(); prune(); }
}
function edit(entry: Entry, content: PersistentComposerDraft, accepted = false) {
  if (accepted ? submissionLocked(entry) : locked(entry)) return;
  // Admission stays locked during recovery, but accepted async work owns its
  // result. Keep it in the local version that recovery reconciles and saves.
  entry.content = content; entry.version++;
  if (!entry.recovering && !entry.unreadable) entry.error = undefined;
  entry.taskError = undefined; entry.application = undefined;
  if (entry.timer) clearTimeout(entry.timer);
  entry.timer = setTimeout(() => {
    void flush(entry).catch(async () => { await recoverInput(entry); if (!locked(entry)) await flush(entry); }).catch(() => {});
  }, 250);
  notify();
}

export async function flushAllSessionComposers() {
  exiting = true;
  notify();
  // Retry cached failures once per close attempt. A new failure must still
  // veto close, and recovery must reconcile the store before retaining edits.
  const retryable = new Set([...entries.values()].filter(entry => entry.error));
  try {
    do {
      for (const entry of entries.values()) {
        const save = async () => {
          while (!entry.retired && entry.tasks.size) await Promise.race([Promise.allSettled([...entry.tasks]), entry.retirement]);
          if (entry.retired) return;
          // Loading/recovery can reveal an error or unsaved input even when the
          // version looked clean at the start of close. Never skip those barriers.
          if (entry.loading) await entry.loading;
          if (entry.recovering) await entry.recovering;
          if (entry.saving) await entry.saving;
          if (entry.retired) return;
          if (entry.error && retryable.delete(entry)) await recoverInput(entry);
          if (entry.version > entry.saved || entry.error) await flush(entry);
        };
        // A committed purge also releases an already-running close barrier.
        try { await Promise.race([save(), entry.retirement]); }
        catch (error) { if (!entry.retired) throw error; }
      }
      // Accepted work may attach a continuation to an owner already visited
      // while another owner's save was pending. Require global quiescence.
    } while ([...entries.values()].some(entry => entry.tasks.size || entry.loading || entry.recovering || entry.saving || entry.version > entry.saved || entry.error));
  } catch (error) { resumeSessionComposerEditing(); throw error; }
}
export function resumeSessionComposerEditing() { exiting = false; notify(); }

export async function beginPersistedComposerSubmission(tabId: string, submissionId: string, requestJSON: string, expectedEdit?: number, key = tabs.get(tabId), owner = tabOwners.get(tabId)) {
  const entry = key ? entries.get(key) : undefined;
  if (!entry) return undefined;
  await flush(entry);
  if (!owner || tabs.get(tabId) !== key || tabOwners.get(tabId) !== owner) throw new Error("reasonix_error:inbox_not_submitted");
  // Accepted input must publish before a submission can freeze this draft.
  // Submission tasks also participate in close draining, but never in this guard.
  if (entry.inputTasks.size) throw new Error("Input is still being prepared; review and send again");
  if (expectedEdit !== undefined && entry.version !== expectedEdit) throw new Error("Input changed while preparing submission; review and send again");
  if (exiting || !entry.state || locked(entry)) throw new Error("Review the saved input before sending");
  const submittedVersion = entry.version;
  entry.registering = true;
  notify();
  try {
    entry.state = await app.BeginSessionComposerSubmission(entry.ref, entry.state.revision, submissionId, requestJSON);
  } catch (error) {
    // The host may have committed despite transport failure. Reconcile before
    // allowing edits or another send; never infer rejection from a lost reply.
    entry.unsubmittedId = submissionId;
    entry.unreadable = true; entry.error = String(error);
    throw error;
  } finally { entry.registering = false; notify(); }
  if (entry.state.conflict) { notify(); throw new Error("The input changed before submission"); }
  notify();
  return (outcome: "accepted" | "not_accepted" | "unknown") => settleSubmission(entry, submissionId, outcome, submittedVersion);
}

async function settleSubmission(entry: Entry, submissionId: string, outcome: "accepted" | "not_accepted" | "unknown", submittedVersion: number) {
    if (entry.retired) return;
    const result = await app.CompleteSessionComposerSubmission(entry.ref, submissionId, outcome);
    if (entry.retired) return;
    if (result.conflict) { notify(); throw new Error("The input changed during submission"); }
    if (!entry.state || BigInt(result.revision) >= BigInt(entry.state.revision)) {
      entry.state = result;
      // A recovery read may already have settled this send and enabled new
      // edits. Only replace the captured version, using the host's actual input.
      if (!result.submissionId && entry.version === submittedVersion) {
        entry.content = parse(result.contentJson); entry.version++; entry.saved = entry.version;
      }
      if (!result.submissionId) entry.settledId = submissionId;
    }
    notify();
}

async function recoverInput(entry: Entry) {
  if (entry.retired) return;
  if (entry.recovering) return entry.recovering;
  if (entry.registering) return;
  entry.recovering = (async () => {
    if (entry.loading) await entry.loading;
    if (entry.saving) await entry.saving.catch(() => {});
    if (entry.retired) return;
    const previous = entry.state;
    const pending = previous?.submissionId || entry.unsubmittedId;
    try {
      let state = await app.GetSessionComposerState(entry.ref);
      if (entry.retired) return;
      // This renderer never invoked send after a failed registration. Only its
      // own registration can be released; an absent receipt alone is not proof.
      if (entry.unsubmittedId && state.submissionId === entry.unsubmittedId) {
        state = await app.CompleteSessionComposerSubmission(entry.ref, entry.unsubmittedId, "not_accepted");
        if (entry.retired) return;
      }
      const content = parse(state.contentJson);
      if (entry.state && BigInt(state.revision) < BigInt(entry.state.revision)) return;
      entry.error = undefined; entry.unreadable = false;
      entry.state = state;
      if (state.submissionId) return;
      if (pending) entry.settledId = pending;
      if (previous?.submissionId && encoded(entry.content) === encoded(parse(previous.contentJson))) {
        entry.content = content; entry.version++; entry.saved = entry.version;
      } else if (encoded(content) === encoded(entry.content)) {
        entry.saved = entry.version;
      } else if (previous && encoded(content) !== encoded(parse(previous.contentJson))) {
        // Continue with this window's input on its next save or explicit send.
        if (entry.version === entry.saved) entry.version++;
      } else if (!previous) {
        entry.content = content; entry.saved = entry.version;
      }
      entry.unsubmittedId = undefined;
    } catch (error) { if (!entry.retired) { entry.error = String(error); entry.unreadable = true; throw error; } }
  })();
  notify();
  try { await entry.recovering; } finally { entry.recovering = undefined; notify(); }
}

export function useSessionComposerPersistence(ref: SessionRef | undefined, tabId: string | undefined) {
  const key = ref?.hostId === "local" ? keyFor(ref) : "";
  const entry = useMemo(() => key && ref ? entryFor(ref) : undefined, [key]);
  useSyncExternalStore(subscribe, snapshot);
  useEffect(() => {
    if (!entry || entry.retired) return;
    entries.set(key,entry);
    entry.users++; entry.touched=Date.now();
    const owner = Symbol();
    if (tabId) { tabs.set(tabId,key); tabOwners.set(tabId,owner); }
    if (!entry.state && !entry.loading) void load(entry);
    return () => {
      if (tabId && tabOwners.get(tabId) === owner) { tabs.delete(tabId); tabOwners.delete(tabId); }
      entry.users--; if (entry.version > entry.saved) void flush(entry).catch(() => {}); prune();
    };
  }, [entry,key,tabId]);
  const target: PersistentComposerTarget | undefined = entry ? {
    draftId:key, generation:entry.generation, initial:entry.content, revision:entry.version,
    isCurrent:(id,generation) => !entry.retired && entries.get(key)===entry && id===key && generation===entry.generation,
    canEdit:() => !exiting && !locked(entry),
    // Already accepted tasks still publish through onPatch while closing.
    onChange:(_id,generation,content) => { if (generation===entry.generation) edit(entry,content); },
    onPatch:(_id,generation,patch) => { if (!entry.retired && generation===entry.generation) edit(entry,{ ...entry.content,...(typeof patch==="function" ? patch(entry.content) : patch) },true); },
    trackTask:(_id,generation,promise,blocksSubmission) => {
      if (!entry.retired && generation===entry.generation) {
        entry.tasks.add(promise); if (blocksSubmission) entry.inputTasks.add(promise);
        void promise.finally(()=>{entry.tasks.delete(promise);entry.inputTasks.delete(promise);prune();}).catch(()=>{});
      }
      return promise;
    },
    // Failed attachment operations remain visible, but do not make the
    // retained input unreadable or prevent its independent durable save.
    onTaskError:(_id,generation,error) => { if (!entry.retired && generation===entry.generation) { entry.taskError=error; notify(); } },
  } : undefined;
  return { target, blocked:entry ? exiting || locked(entry) : false, error:entry?.error, taskError:entry?.taskError,
    dismissTaskError:() => { if (entry) { entry.taskError=undefined; notify(); } },
    application:entry?.application,
    setApplication:(value:ModelApplicationDetails|undefined)=>{if(entry){entry.application=value;notify();}},
    reportSubmissionError:(error:unknown) => { if (entry) {entry.application=modelApplicationError(error);notify();} },
    // Editable retained text does not authorize restoring a mode from older history.
    goalDraft:!entry?.state?.historyChanged && entry?.content.goalDraft,
    setGoalDraft:(enabled:boolean) => { if (entry && !exiting) edit(entry,{...entry.content,goalDraft:enabled}); },
    settleSubmission:async (id:string) => { if (entry?.state?.submissionId === id) await settleSubmission(entry,id,"accepted",entry.version); },
    attention: Boolean(entry?.state?.submissionId),
    recovering: Boolean(entry?.recovering),
    settledId: entry?.settledId,
    retry:async () => { if (!entry) return; await recoverInput(entry); if (!locked(entry)) await flush(entry); },
  };
}
