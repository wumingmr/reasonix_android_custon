import { randomToken, type DocumentRegistry, type FrameBinding } from "./documents.js";
import type { GuestFrame, GuestPage } from "./guestView.js";
import { scriptCall, SNAPSHOT_SCRIPT_SOURCE } from "./pageScripts.js";
import type { SnapshotOutput } from "./snapshotScript.js";
import { FrameRuntime, runChildFrame } from "./frameRuntime.js";
import { browserFailure } from "./errors.js";

export const REGISTRY_KEY = "__reasonixBrowserRegistry";
export const ISOLATED_WORLD = 1;
export const MAX_SNAPSHOT_NODES = 4000;

export interface SnapshotResult {
  documentToken: string;
  url: string;
  title: string;
  tree: string;
  refs: number;
}

// The main frame uses Electron's isolated world; children use a verified CDP
// isolated context, with an owned session for out-of-process iframes.
export function runInFrame(page: GuestPage, frame: GuestFrame, code: string, runtime?: FrameRuntime): Promise<unknown> {
  if (frame.frameTreeNodeId === page.mainFrame.frameTreeNodeId) return page.executeJavaScriptInIsolatedWorld(ISOLATED_WORLD, [{ code }]);
  return runtime ? runtime.run(frame, code) : runChildFrame(page, frame, code);
}

export function findFrame(page: GuestPage, frameTreeNodeId: number): GuestFrame | null {
  const main = page.mainFrame;
  if (main.frameTreeNodeId === frameTreeNodeId) return main;
  for (const frame of main.framesInSubtree) {
    if (frame.frameTreeNodeId === frameTreeNodeId && !frame.detached) return frame;
  }
  return null;
}

function isSnapshotOutput(value: unknown): value is SnapshotOutput {
  if (typeof value !== "object" || value === null) return false;
  const out = value as Record<string, unknown>;
  return typeof out.docId === "string" && typeof out.tree === "string" && typeof out.refs === "number" && typeof out.nodes === "number";
}

function clipURL(url: string): string {
  return url.length > 120 ? `${url.slice(0, 119)}…` : url;
}

function indent(tree: string): string {
  return tree.split("\n").map((line) => `  ${line}`).join("\n");
}

async function within<T>(promise: Promise<T>, milliseconds: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try { return await Promise.race([promise, new Promise<never>((_resolve, reject) => { timer = setTimeout(() => reject(browserFailure("page_not_ready", "DOM observation deadline exceeded")), Math.max(1, milliseconds)); })]); }
  finally { if (timer) clearTimeout(timer); }
}

export async function takeSnapshot(page: GuestPage, tabId: string, epoch: number, selector: string, documents: DocumentRegistry, verify: () => void = () => {}): Promise<SnapshotResult> {
  const snapshotId = randomToken(8);
  const main = page.mainFrame;
  let mainRaw: unknown;
  try { mainRaw = await within(runInFrame(page, main, scriptCall(SNAPSHOT_SCRIPT_SOURCE, { key: REGISTRY_KEY, snapshotId, prefix: "", selector, budget: MAX_SNAPSHOT_NODES })), 3000); }
  catch (error) { if (typeof error === "object" && error !== null && "code" in error) throw error; throw browserFailure("script_runtime_error", String(error).slice(0, 500)); }
  if (!isSnapshotOutput(mainRaw)) throw new Error("snapshot script returned no tree");
  const frames: FrameBinding[] = [{ prefix: "", frameTreeNodeId: main.frameTreeNodeId, docId: mainRaw.docId }];
  const sections = [mainRaw.tree];
  let refs = mainRaw.refs;
  let budget = MAX_SNAPSHOT_NODES - mainRaw.nodes;
  let index = 0;
  const childrenDeadline = Date.now() + 1000;
  const children = main.framesInSubtree.filter(frame => frame.frameTreeNodeId !== main.frameTreeNodeId && !frame.detached);
  const runtime = children.length ? new FrameRuntime(page, childrenDeadline) : undefined;
  try {
    for (const frame of children) {
      if (budget <= 0 || Date.now() >= childrenDeadline) break;
      index += 1;
      const prefix = `f${index}`;
      let raw: unknown;
      try {
        raw = await runInFrame(page, frame, scriptCall(SNAPSHOT_SCRIPT_SOURCE, { key: REGISTRY_KEY, snapshotId, prefix, selector: "", budget }), runtime);
      } catch (error) {
        sections.push(`frame ${prefix} unavailable: ${error instanceof Error ? error.message.slice(0, 200) : "isolated runtime unavailable"}`);
        continue;
      }
      if (!isSnapshotOutput(raw)) continue;
      frames.push({ prefix, frameTreeNodeId: frame.frameTreeNodeId, docId: raw.docId });
      refs += raw.refs;
      budget -= raw.nodes;
      if (raw.tree === "") continue;
      sections.push(`frame ${prefix} ${JSON.stringify(clipURL(frame.url))}\n${indent(raw.tree)}`);
    }
  } finally { await runtime?.close(); }
  verify();
  const documentToken = documents.issue({ tabId, epoch, snapshotId, frames });
  return { documentToken, url: page.getURL(), title: page.getTitle(), tree: sections.join("\n"), refs };
}
