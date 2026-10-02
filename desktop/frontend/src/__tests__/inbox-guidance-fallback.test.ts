import assert from "node:assert/strict";
import { enqueueComposerGuidance, enqueueGuidanceForTarget, enqueueTrackedGuidance } from "../lib/inboxGuidanceSubmit";
import { resolveActiveTurnId, steerInboxItemForActiveTurn } from "../lib/inboxSubmit";
import { followupNotSubmitted, type PendingFollowup } from "../lib/pendingFollowup";
import type { AppBindings } from "../lib/bridge";
import type { InboxQueueResult } from "../lib/inboxQueueCommands";

const target = { tabId: "tab", sessionPath: "session-id:original", generation: 1, selection: 0, remote: false };
const request: PendingFollowup = { target, tabId: "tab", key: "one-request", display: "guide", submit: "guide", draft: "guide" };
const receipt = { itemId: "item", disposition: "queued_followup", position: 1, paused: false };
let reads = 0;
let writes: unknown[][] = [];
const bindings = (overrides: Partial<AppBindings> = {}) => ({
  ListTabs: async () => { reads++; return []; },
  EnqueueInboxFollowupForTarget: async (...args: unknown[]) => { writes.push(args); return receipt; },
  InboxQueueForTarget: async (...args: unknown[]) => { writes.push(args); return { outcome: "applied", receipt }; },
  EnqueueInboxFollowup: async () => { throw new Error("unsafe tab-only fallback"); },
  ...overrides,
} as AppBindings);

assert.equal(await resolveActiveTurnId(bindings(), "tab", "observed"), "observed");
assert.equal(reads, 0);
assert.deepEqual(await enqueueComposerGuidance(bindings(), request, false, "observed"), receipt);
assert.deepEqual(writes.pop(), [target, { kind: "enqueue_steer", text: "guide", display: "guide", turnId: "observed", idempotencyKey: "one-request" }]);
assert.equal(reads, 0, "known turn must not be replaced by a successor query");
await enqueueGuidanceForTarget(bindings(), target, "tab", "captured guidance", "observed");
const [capturedTarget, capturedCommand] = writes.pop() as [unknown, { text: string; display: string; turnId: string; idempotencyKey: string }];
assert.deepEqual(capturedTarget, target);
assert.equal(capturedCommand.text, "captured guidance");
assert.equal(capturedCommand.display, "captured guidance");
assert.equal(capturedCommand.turnId, "observed");
assert.match(capturedCommand.idempotencyKey, /^guidance-/);

for (const ListTabs of [async () => [], async () => { throw new Error("discovery unavailable"); }]) {
  writes = [];
  await enqueueComposerGuidance(bindings({ ListTabs }), request, false);
  assert.deepEqual(writes, [[target, "guide", "guide", [], "one-request"]]);
}

for (const InboxQueueForTarget of [undefined, async () => ({ outcome: "unavailable", reason: "unsupported" })]) {
  writes = [];
  await enqueueComposerGuidance(bindings({ InboxQueueForTarget } as Partial<AppBindings>), request, false, "observed");
  assert.deepEqual(writes, [[target, "guide", "guide", [], "one-request"]]);
}

for (const result of [{ outcome: "unavailable", reason: "session_changed", snapshot: {} }, { outcome: "applied", snapshot: {} }] satisfies InboxQueueResult[]) {
  writes = [];
  await assert.rejects(enqueueComposerGuidance(bindings({ InboxQueueForTarget: async () => result } as Partial<AppBindings>), request, false, "observed"), (error) => {
    assert.equal(followupNotSubmitted(error), false, "uncertain writes must retain the request key");
    return /receipt unconfirmed/.test(String(error));
  });
  assert.equal(writes.length, 0, "never redirect or resend an uncertain write");
}
await assert.rejects(enqueueComposerGuidance(bindings({ InboxQueueForTarget: async () => { throw new Error("reply lost"); } }), request, false, "observed"), /reply lost/);
assert.equal(writes.length, 0);
await assert.rejects(enqueueComposerGuidance(bindings({ EnqueueInboxFollowupForTarget: undefined }), request, false), /target submission unavailable/);

let steered: string[] = [];
await steerInboxItemForActiveTurn({ ListTabs: async () => { throw new Error("must not refresh"); }, SteerInboxItemForTurn: async (...args: string[]) => { steered = args; return receipt; } } as unknown as AppBindings, "tab", "item", "observed");
assert.deepEqual(steered, ["tab", "observed", "item"]);
let posts = 0;
const lostReply = bindings({
  InboxQueueForTarget: async () => { posts++; throw new Error("accepted reply lost"); },
  LookupInboxFollowupForTarget: async (captured, key) => {
    assert.deepEqual(captured, target);
    assert.equal(key, request.key);
    return receipt;
  },
});
await assert.rejects(enqueueTrackedGuidance(lostReply, request, "observed"), /reply lost/);
assert.deepEqual(await enqueueTrackedGuidance(lostReply, { ...request, key: "retry-key" }, "successor"), receipt);
assert.equal(posts, 1, "retry confirms the original key without another POST");
await assert.rejects(enqueueTrackedGuidance(lostReply, request, "observed"), /reply lost/);
await assert.rejects(enqueueTrackedGuidance(lostReply, { ...request, key: "different-key", submit: "different instruction" }, "observed"), /reply lost/);
assert.equal(posts, 3, "a different draft needs its own submission, not an old receipt");
console.log("inbox guidance target, turn, fallback and receipt regressions passed");
