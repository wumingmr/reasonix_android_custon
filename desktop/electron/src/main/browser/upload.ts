import type { ActResult } from "./actions.js";
import type { GuestDebugger, GuestPage } from "./guestView.js";
import type { LocatedRef } from "./refResolver.js";
import { REGISTRY_KEY } from "./snapshot.js";
import { acquireDebugger, type DebuggerSender } from "./debuggerLease.js";
import { runChildFrame } from "./frameRuntime.js";

const OBJECT_GROUP = "reasonix-upload";

// The executable source is fixed. Snapshot metadata crosses the CDP boundary
// only as call arguments, never as JavaScript source text.
const FIND_UPLOAD_NODE = `function (key, docId, snapshotId, ref) {
  const registry = globalThis[key];
  if (!registry || registry.docId !== docId || registry.snapshotId !== snapshotId) return null;
  const element = registry.refs.get(ref);
  return element && element.isConnected && element.tagName === 'INPUT' && element.type === 'file' ? element : null;
}`;

interface ExecutionContext {
  id: number;
  auxData?: { isDefault?: boolean; frameId?: string };
}

function objectIdOf(result: unknown): string | null {
  const objectId = (result as { result?: { objectId?: unknown } } | null)?.result?.objectId;
  return typeof objectId === "string" ? objectId : null;
}

// Runtime.enable replays executionContextCreated for every live context
// before its own reply, so a listener registered around it sees them all.
async function executionContexts(dbg: GuestDebugger, send: DebuggerSender): Promise<ExecutionContext[]> {
  const contexts: ExecutionContext[] = [];
  const listener = (_event: unknown, method: string, params: unknown) => {
    if (method !== "Runtime.executionContextCreated") return;
    const context = (params as { context?: ExecutionContext } | null)?.context;
    if (context && typeof context.id === "number") contexts.push(context);
  };
  dbg.on("message", listener);
  try {
    await send("Runtime.enable");
  } finally {
    dbg.removeListener("message", listener);
  }
  return contexts;
}

// Find the original registry node in its execution context, including the
// main frame's isolated world. A CSS path would silently select a replacement
// input after a rerender or a navigation.
async function findObjectId(dbg: GuestDebugger, located: LocatedRef, owned: boolean, send: DebuggerSender): Promise<string | null> {
  const args = [REGISTRY_KEY, located.binding.docId, located.snapshotId, located.ref].map((value) => ({ value }));
  try {
    for (const context of await executionContexts(dbg, send)) {
      if (located.isMainFrame && context.auxData?.isDefault) continue;
      try {
        const objectId = objectIdOf(await send("Runtime.callFunctionOn", { functionDeclaration: FIND_UPLOAD_NODE, executionContextId: context.id, arguments: args, objectGroup: OBJECT_GROUP }));
        if (objectId) return objectId;
      } catch {
        // A context that vanished mid-walk is simply not the one we want.
      }
    }
  } finally {
    if (owned) await send("Runtime.disable").catch(() => undefined);
  }
  return null;
}

export async function uploadFiles(page: GuestPage, located: LocatedRef, files: string[], verify: () => void, dispatch: () => void): Promise<ActResult> {
  if (located.tag !== "input" || located.type !== "file") return { executed: false, reason: "element is not a file input" };
  const dbg = page.debugger;
  if (!located.isMainFrame) {
    return await runChildFrame(page, located.frame, "", async (contextId, sessionId, send) => {
      const args = [REGISTRY_KEY, located.binding.docId, located.snapshotId, located.ref].map(value => ({ value }));
      try {
        const objectId = objectIdOf(await send("Runtime.callFunctionOn", { functionDeclaration: FIND_UPLOAD_NODE, executionContextId: contextId, arguments: args, objectGroup: OBJECT_GROUP }, sessionId));
        if (!objectId) return { executed: false, reason: "file input reference expired" };
        verify(); dispatch();
        await send("DOM.setFileInputFiles", { objectId, files }, sessionId);
        return { executed: true };
      } finally { await send("Runtime.releaseObjectGroup", { objectGroup: OBJECT_GROUP }, sessionId).catch(() => undefined); }
    }) as ActResult;
  }
  const release = acquireDebugger(dbg);
  try {
    const objectId = await findObjectId(dbg, located, release.owned, release.send);
    if (!objectId) return { executed: false, reason: "file input could not be located in the page" };
    verify();
    dispatch();
    await release.send("DOM.setFileInputFiles", { objectId, files });
    await release.send("Runtime.releaseObjectGroup", { objectGroup: OBJECT_GROUP }).catch(() => undefined);
    return { executed: true };
  } finally {
    release();
  }
}
