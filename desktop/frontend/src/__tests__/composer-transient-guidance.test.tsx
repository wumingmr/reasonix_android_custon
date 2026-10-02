// A transient inbox-target fence (session switching, reconnecting, route
// adoption) clears by itself. The composer must wait it out and deliver the
// message instead of surfacing "the message was not sent" for a window the
// user cannot act on.
import assert from "node:assert/strict";
import { act } from "react";
import { installDom, installBridgeApp, renderComposer } from "./composerInboxHarness";
import { runtimeStateStore, type RuntimeProjection } from "../lib/runtimeStateStore";
import { acceptRuntimeState } from "../lib/runtimeStateReducer";
import { pendingFollowups, followupSessionKey, type InboxTarget } from "../lib/pendingFollowup";
import { formatInboxError } from "../lib/inboxError";
const TRANSIENT_INBOX_TARGET_CODE = "inbox_target_transient";
const TRANSIENT_GUIDANCE_RETRY_DELAYS_MS = [150, 350, 500, 500];

const pendingSession = followupSessionKey("session-a");
const DEAD_END_COPY = "Refresh the session";

const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0));
const settle = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms));

async function paste(text: string) {
  await act(async () => {
    const event = new window.Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "clipboardData", { value: { files: [], items: [], types: ["text/plain"], getData: () => text } });
    document.querySelector("textarea.composer__input:not([aria-hidden=true])")!.dispatchEvent(event);
    await flush();
  });
}
async function send() {
  await act(async () => { (document.querySelector(".composer__btn--send") as HTMLButtonElement).click(); await flush(); });
}
function transientError() {
  return Object.assign(new Error(`reasonix_error:${TRANSIENT_INBOX_TARGET_CODE}`), { code: "business", data: { transient: true } });
}
function state(scenario: number, phase: "idle" | "executing" | "finishing", revision: number): RuntimeProjection {
  return { epoch: `transient-test-${scenario}`, revision, topics: [], sessions: [{
    tabId: "tab-a", scope: "project", workspaceRoot: "/repo", topicId: "topic", sessionPath: "session-a", sessionGeneration: 1,
    open: true, remote: true, freshness: "synced", state: { schemaVersion: 1, runtimeEpoch: "controller", revision,
      phase, running: phase !== "idle", turnId: "turn", turnStatus: "completed", turnEventSeq: 3, turnStartedAt: 0,
      pendingPrompt: false, cancelRequested: false, cancellable: phase === "executing", backgroundJobs: 0, activity: "" },
  }] };
}
const target: InboxTarget = { tabId: "tab-a", sessionPath: "session-a", generation: 7, selection: 3, remote: true };
const receipt = { itemId: "item-1", disposition: "queued_followup", position: 0, paused: false };

// The classifier and the copy table must recognize the bridge's transient mark.
{
  assert.equal(formatInboxError(new Error(`reasonix_error:${TRANSIENT_INBOX_TARGET_CODE}`), "en").includes("switching"), true);
}

// 1. A capture-phase fence is waited out: the send completes normally.
{
  installDom();
  let captures = 0, posts = 0;
  installBridgeApp({
    CaptureInboxTarget: async () => { captures++; if (captures <= 2) throw transientError(); return target; },
    EnqueueInboxFollowupForTarget: async () => { posts++; return receipt; },
    InboxSnapshot: async () => ({ revision: 1, paused: false, recovered: false, items: [], itemsCount: 0, bytes: 0, maxItems: 64, maxBytes: 1024 }),
  });
  const SCENARIO = 1;
  await renderComposer({ inboxSessionPath: "session-a" });
  await act(async () => { acceptRuntimeState(runtimeStateStore, state(SCENARIO, "finishing", 1), true); await flush(); });
  await paste("held during switch");
  await send();
  await act(async () => { await settle(TRANSIENT_GUIDANCE_RETRY_DELAYS_MS[0] + TRANSIENT_GUIDANCE_RETRY_DELAYS_MS[1] + 80); });
  assert.equal(captures, 3, "capture retried until the tab settled");
  assert.equal(posts, 1, "the settled send enqueued exactly once");
  assert.equal(document.body.textContent?.includes(DEAD_END_COPY), false, "no dead-end copy for a self-clearing window");
}

// 2. An enqueue-phase fence holds the request and auto-delivers on retry.
{
  installDom();
  let posts = 0;
  installBridgeApp({
    CaptureInboxTarget: async () => target,
    EnqueueInboxFollowupForTarget: async () => { posts++; if (posts === 1) throw transientError(); return receipt; },
    InboxSnapshot: async () => ({ revision: 1, paused: false, recovered: false, items: [], itemsCount: 0, bytes: 0, maxItems: 64, maxBytes: 1024 }),
  });
  const SCENARIO = 2;
  await renderComposer({ inboxSessionPath: "session-a" });
  await act(async () => { acceptRuntimeState(runtimeStateStore, state(SCENARIO, "finishing", 1), true); await flush(); });
  await paste("retried until queued");
  await send();
  await act(async () => { await settle(TRANSIENT_GUIDANCE_RETRY_DELAYS_MS[0] + 80); });
  assert.equal(posts, 2, "the held request retried the enqueue");
  assert.equal(pendingFollowups.get(pendingSession), undefined, "a delivered request clears its pending identity");
  assert.equal(document.body.textContent?.includes(DEAD_END_COPY), false, "delivery never surfaced the dead-end copy");
}

// 3. Outlasting the bound keeps the request visible for later reconciliation
//    instead of telling the user to refresh.
{
  installDom();
  let posts = 0;
  installBridgeApp({
    CaptureInboxTarget: async () => target,
    EnqueueInboxFollowupForTarget: async () => { posts++; throw transientError(); },
    InboxSnapshot: async () => ({ revision: 1, paused: false, recovered: false, items: [], itemsCount: 0, bytes: 0, maxItems: 64, maxBytes: 1024 }),
  });
  const SCENARIO = 3;
  await renderComposer({ inboxSessionPath: "session-a" });
  await act(async () => { acceptRuntimeState(runtimeStateStore, state(SCENARIO, "finishing", 1), true); await flush(); });
  await paste("switching forever");
  await send();
  await act(async () => { await settle(TRANSIENT_GUIDANCE_RETRY_DELAYS_MS.reduce((sum, ms) => sum + ms, 0) + 200); });
  assert.equal(posts, 1 + TRANSIENT_GUIDANCE_RETRY_DELAYS_MS.length, "the retry bound is finite");
  assert.ok(pendingFollowups.get(pendingSession), "an undelivered request stays pending for reconciliation");
  assert.equal(document.body.textContent?.includes(DEAD_END_COPY), false, "exhaustion still avoids the dead-end copy");
  // The pending store is module-global: retire this scenario's leftover so the
  // next one starts from a clean session identity.
  pendingFollowups.clear(pendingSession, pendingFollowups.get(pendingSession));
}

// 4. Permanent refusals keep the historical behavior (clear + report).
{
  installDom();
  installBridgeApp({
    CaptureInboxTarget: async () => target,
    EnqueueInboxFollowupForTarget: async () => { throw new Error("reasonix_error:inbox_item_too_large"); },
    InboxSnapshot: async () => ({ revision: 1, paused: false, recovered: false, items: [], itemsCount: 0, bytes: 0, maxItems: 64, maxBytes: 1024 }),
  });
  const SCENARIO = 4;
  await renderComposer({ inboxSessionPath: "session-a" });
  await act(async () => { acceptRuntimeState(runtimeStateStore, state(SCENARIO, "finishing", 1), true); await flush(); });
  await paste("permanently refused");
  await send();
  await act(async () => { await settle(60); });
  assert.equal(pendingFollowups.get(pendingSession), undefined, "permanent refusals drop the pending identity");
  assert.equal(document.body.textContent?.includes("too large"), true, "permanent refusals stay visible to the user");
}

console.log("composer transient guidance: retries a self-clearing fence and keeps the message visible");
// Mounted roots leave timers behind; exit once the scenarios above ran.
process.exit(0);
