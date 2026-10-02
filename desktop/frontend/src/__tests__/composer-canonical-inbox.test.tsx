import assert from "node:assert/strict";
import { act } from "react";
import { installDom, installBridgeApp, renderComposer } from "./composerInboxHarness";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { runtimeStateStore, type RuntimeProjection } from "../lib/runtimeStateStore";
import type { InboxTarget } from "../lib/pendingFollowup";
import type { InboxQueueRequest } from "../lib/inboxQueueCommands";

async function waitFor(check: () => boolean) {
  for (let attempt = 0; attempt < 100 && !check(); attempt++) {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 10)); });
  }
  assert.ok(check(), document.body.textContent || "composer did not become ready");
}

for (const phase of ["executing", "finishing"] as const) {
  const dom = installDom();
  const sessionId = `canonical-${phase}`;
  const ref = { hostId: "local", sessionId };
  const route = `session-id:${sessionId}`;
  const backend = makeSessionUIMock(async () => {});
  const target: InboxTarget = { tabId: "tab-a", sessionPath: route, generation: 3, selection: 0, remote: false };
  let queues = 0, steers = 0, lookups = 0, direct = 0;
  let lost = false, conflict = false, lastKey = "";
  const receipt = (key: string) => ({ itemId: key, disposition: "queued_followup", position: 1, paused: false });
  installBridgeApp({
    ...backend,
    BeginSessionComposerSubmission: async (...args: Parameters<typeof backend.BeginSessionComposerSubmission>) => {
      if (conflict) {
        conflict = false;
        const state = await backend.GetSessionComposerState(ref);
        await backend.SaveSessionComposerState({ ref, expectedRevision: state.revision, contentVersion: 1, contentJson: '{"text":"another window"}' });
        throw new Error("session_operation:input_conflict:The input changed");
      }
      return backend.BeginSessionComposerSubmission(...args);
    },
    CaptureInboxTarget: async (tabId: string, path: string) => {
      assert.equal(tabId, "tab-a"); assert.equal(path, route);
      return target;
    },
    EnqueueInboxFollowupForTarget: async (captured: InboxTarget, _display: string, _submit: string, _invocations: unknown, key: string) => {
      assert.deepEqual(captured, target); queues++; lastKey = key;
      if (lost) throw new Error("accepted reply lost");
      return receipt(key);
    },
    InboxQueueForTarget: async (captured: InboxTarget, request: InboxQueueRequest) => {
      assert.deepEqual(captured, target); assert.equal(request.kind, "enqueue_steer");
      assert.equal(request.turnId, "active-turn"); steers++;
      return { outcome: "applied", receipt: { ...receipt(request.idempotencyKey!), disposition: "steer_accepted" } };
    },
    LookupInboxFollowupForTarget: async (captured: InboxTarget, key: string) => {
      assert.deepEqual(captured, target); assert.equal(key, lastKey); lookups++;
      return receipt(key);
    },
    ListTabs: async () => [{ id: "tab-a", session: ref, sessionPath: "", turnId: "active-turn" }],
    InboxSnapshot: async () => ({ revision: 1, sessionPath: route, mutationsSupported: true, items: [], itemsCount: 0, paused: false }),
  });
  const projection: RuntimeProjection = { epoch: sessionId, revision: 1, topics: [], sessions: [{
    tabId: "tab-a", scope: "global", workspaceRoot: "", topicId: "topic", sessionId, sessionPath: "", sessionGeneration: 3,
    open: true, remote: false, freshness: "synced", state: { schemaVersion: 1, runtimeEpoch: "runtime", activityRevision: 1, revision: 1,
      phase, running: true, turnId: "active-turn", turnStatus: "running", turnEventSeq: 1, pendingPrompt: false,
      cancelRequested: false, cancellable: phase === "executing", backgroundJobs: 0, activity: "streaming" },
  }] };
  runtimeStateStore.commit(projection);
  const view = await renderComposer({ running: false, formalSessionRef: ref, sessionKey: sessionId,
    sessionIdentity: { session: ref, sessionPath: "", sessionGeneration: 3 }, inboxSessionPath: route,
    onSend: () => { direct++; } });
  let insert = 0;
  const input = () => document.querySelector<HTMLTextAreaElement>("textarea.composer__input:not([aria-hidden=true])")!;
  async function compose(text: string) {
    await waitFor(() => !!input() && !input().disabled);
    await view.rerender({ insertRequest: { id: ++insert, text, mode: "replace" } });
  }
  async function click(selector: string) {
    const button = document.querySelector<HTMLButtonElement>(selector);
    assert.ok(button && !button.disabled, `missing enabled ${selector}`);
    await act(async () => { button.click(); });
  }
  try {
    await compose("queue while model runs");
    await click(".composer__btn--send");
    await waitFor(() => queues === 1 && input().value === "" && !input().disabled);
    await compose("guide current turn");
    // Finishing sessions must retain the instruction as a next-turn message.
    await click(phase === "executing" ? ".composer__queue-steer" : ".composer__btn--send");
    await waitFor(() => queues + steers === 2 && input().value === "" && !input().disabled);
    assert.equal(steers, phase === "executing" ? 1 : 0);
    assert.equal(direct, 0, "canonical runtime phase must prevent an ordinary direct send");

    await compose("retain conflicted guidance");
    conflict = true;
    const sendButton = phase === "executing" ? ".composer__queue-steer" : ".composer__btn--send";
    await click(sendButton);
    await waitFor(() => !input().disabled);
    assert.doesNotMatch(document.body.textContent || "", /核实提交状态|使用已保存版本|保留本窗口版本/);
    assert.equal(input().value, "retain conflicted guidance");
    assert.equal(queues + steers, 2, "registration rejection never reached the inbox");
    await click(sendButton);
    await waitFor(() => queues + steers === 3 && input().value === "" && !input().disabled);

    await compose("recover a lost queue receipt");
    lost = true;
    await click(".composer__btn--send");
    await waitFor(() => input().disabled && !!document.querySelector(".session-draft-surface__error"));
    const before = queues;
    await click(".composer__btn--send");
    await waitFor(() => lookups === 1 && input().value === "" && !input().disabled);
    assert.equal(queues, before, "receipt recovery must not post the instruction twice");
    assert.equal((await backend.GetSessionComposerState(ref)).submissionId, undefined);
    assert.equal(document.querySelector(".session-draft-surface__error"), null);
  } finally {
    await act(async () => view.root.unmount());
    dom.window.close();
  }
}
console.log("canonical composer: running queue, current-turn guidance, finishing and lost receipt recovery passed");
