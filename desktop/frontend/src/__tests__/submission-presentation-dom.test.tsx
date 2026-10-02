import assert from "node:assert/strict";
import { act } from "react";
import { createTranscriptHarness } from "./transcript-dom-harness";
import type { Item } from "../lib/useController";
import type { LocalSubmission } from "../lib/localSubmissionState";
import { initialState, reducer } from "../lib/useController";
import { orderedLocalSubmissions } from "../lib/localSubmissionState";

const harness = await createTranscriptHarness({ deterministic: true });
const pending: LocalSubmission = { submissionId: "send", localId: "optimistic", text: "unique question", createdAt: 1, sequence: 0, status: "sending" };
const confirmed: Item = { kind: "user", id: "m:durable", messageId: "durable", submissionId: "send", text: "unique question" };
try {
  await harness.loadModule("/src/components/ChatToolBody.tsx");
  await harness.render([], { tabId: "identity-dom", localSubmissions: [pending], localSubmissionSendRevision: 1, running: true });
  await harness.settle();
  const selector = '[data-chat-kind="user"]';
  const before = harness.container.querySelector(selector);
  await harness.render([confirmed], { tabId: "identity-dom", localSubmissions: [], localSubmissionSendRevision: 1, running: true });
  await harness.settle();
  assert.equal(harness.container.textContent?.split("unique question").length, 2, "one bubble after durable handoff");
  assert.ok(before);
  assert.equal(harness.container.querySelector(selector), before, "mounted user identity remains stable");
  // React may render only the final state of identity binding + formal install.
  let batched = reducer({ ...initialState, transcriptProtocol: 2 }, { type: "user", seq: 0, text: "batched question", submissionId: "batched" });
  const answer: Item = { kind: "assistant", id: "m:answer", text: "answer", reasoning: "process", streaming: false, turnFinal: true };
  await harness.render([answer], { tabId: "batched-dom", localSubmissions: orderedLocalSubmissions(batched), running: true });
  await harness.settle();
  const mounted = ["user", "process", "tail"].map(kind => harness.container.querySelector(`[data-chat-kind="${kind}"]`));
  batched = reducer(batched, { type: "event", e: { kind: "user_message", source: "executor", messageId: "bound", submissionId: "batched" } });
  batched = reducer(batched, { type: "transcript_records", confirmedUsers: [{ messageId: "bound" }], projection: {
    items: [{ kind: "user", id: "m:bound", messageId: "bound", text: "batched question" }, answer], removeIds: [],
    startTurn: 1, endTurn: 1, totalTurns: 1, hasOlder: false, hasNewer: false, revision: 1, revisionKnown: true, digest: "cut",
  } });
  await harness.render(batched.items, { tabId: "batched-dom", localSubmissions: [], visibleSubmissionHandoffs: batched.visibleSubmissionHandoffs, running: true });
  await harness.settle();
  for (const [index, kind] of ["user", "process", "tail"].entries()) {
    assert.ok(mounted[index], `${kind} was mounted`);
    assert.equal(harness.container.querySelector(`[data-chat-kind="${kind}"]`), mounted[index], `${kind} survives a batched message-ID-only handoff`);
  }
  await harness.render([confirmed], { tabId: "identity-dom", localSubmissions: [{ ...pending, localId: "another", submissionId: "send-again", sequence: 1 }], localSubmissionSendRevision: 2, running: true });
  await harness.settle();
  assert.equal(harness.container.textContent?.split("unique question").length, 3, "same text sent twice remains visible twice");
  await harness.render([
    { ...confirmed, id: "m:first-shared", messageId: "first-shared", submissionId: "shared" },
    { ...confirmed, id: "m:second-shared", messageId: "second-shared", submissionId: "shared" },
  ], { tabId: "identity-dom", localSubmissions: [], localSubmissionSendRevision: 2, running: false });
  await harness.settle();
  assert.equal(harness.container.querySelectorAll(selector).length, 2, "different durable messages never share a DOM key even when submission ids collide");
  const oldPage: Item = { kind: "user", id: "m:old-page", messageId: "old-page", text: "old page question" };
  const pendingLatest = { ...pending, text: "latest pending question", submissionId: "latest-pending", localId: "latest-local" };
  await harness.render([oldPage], { tabId: "identity-dom", localSubmissions: [pendingLatest], localSubmissionSendRevision: 3,
    hasNewerHistory: true, running: true });
  await harness.settle();
  assert.equal(harness.container.textContent?.includes("latest pending question"), false, "reading an old page does not insert the latest local echo");
  await harness.render([{ kind: "user", id: "m:latest", messageId: "latest", submissionId: "latest-pending", text: "latest pending question" }],
    { tabId: "identity-dom", localSubmissions: [], localSubmissionSendRevision: 3, hasNewerHistory: false, running: false });
  await harness.settle();
  assert.equal(harness.container.textContent?.split("latest pending question").length, 2, "returning to latest renders one canonical message");
  for (const state of ["timed_out", "cancelled", "completed"] as const) {
    const shell: Item = { kind: "tool", id: state, name: "bash", args: '{"command":"echo test"}', output: "test", status: "done",
      execution: { kind: "shell", supportsAndAnd: true, state, exitCode: 0 } };
    await harness.render([shell], { tabId: "identity-dom", geometrySessionKey: state, localSubmissions: [pending] });
    await harness.settle();
    const row = harness.container.querySelector<HTMLElement>(".chat-tool")!;
    const dot = row.querySelector(".dsh-StateDot-dot")?.getAttribute("data-state");
    assert.equal(dot, state === "completed" ? "done" : state === "cancelled" ? "warning" : "error");
    await act(async () => row.querySelector<HTMLElement>("[data-disclosure-row]")!.click());
    await harness.settle();
    assert.equal(row.dataset.state, state === "completed" ? "done" : state === "cancelled" ? "stopped" : "error");
    assert.ok(row.querySelector("[data-terminal]"), "structured shell detail is mounted");
    const dots = [...row.querySelectorAll(".dsh-StateDot-dot")];
    assert.equal(dots.length, 1, "expanded disclosure replaces its dot with a chevron");
    assert.ok(dots.every(node => node.getAttribute("data-state") === dot));
  }
  console.log("DOM: same-frame identity dedup and shared shell presentation passed");
} finally { await harness.unmount(); await harness.close(); }
