import assert from "node:assert/strict";
import { createRuntimeNotifications } from "../lib/runtimeNotifications";
import { runtimeStateStore, type RuntimeSession } from "../lib/runtimeStateStore";
import { t } from "../lib/i18n";

let alerts = 0;
let revision = 0;
const owner = createRuntimeNotifications(() => ({ activeTabId: "B", t, showToast() { alerts++; } }));
function session(index: number): RuntimeSession {
  return { tabId: `detached:${index}`, scope: "global", workspaceRoot: "", topicId: "", sessionPath: "",
    sessionGeneration: 1, open: false, remote: false, freshness: "synced",
    state: { schemaVersion: 1, runtimeEpoch: `runtime:${index}`, activityRevision: 1, revision: 1,
      phase: "executing", running: true, turnId: `turn:${index}`, turnStatus: "in_progress", turnEventSeq: 1,
      pendingPrompt: true, pendingInteractions: [{ requestId: "1", kind: "ask", turnId: `turn:${index}`,
        runtimeEpoch: `runtime:${index}`, headId: "head" }],
      cancelRequested: false, cancellable: true, backgroundJobs: 0, activity: "" } };
}
function publish(sessions: RuntimeSession[]) {
  runtimeStateStore.commit({ epoch: "fixture", revision: ++revision, topics: [], sessions });
}
publish([]);
owner.start();
try {
  publish([session(0)]);
  assert.equal(alerts, 1);
  for (let index = 1; index <= 600; index++) {
    owner.accept({ event: { kind: "approval_request", tabId: "C", turnId: `other:${index}`, approval: { id: "1" } } });
  }
  assert.equal(alerts, 601);
  publish([session(0)]);
  assert.equal(alerts, 601, "history eviction must not re-alert a still-pending background question");
  const manyPending = Array.from({ length: 600 }, (_, index) => session(index + 1000));
  publish(manyPending);
  assert.equal(alerts, 1201);
  publish(manyPending);
  assert.equal(alerts, 1201, "more pending requests than the history cap cannot create an alert storm");
  publish(manyPending.map(item => ({ ...item, freshness: "unknown" })));
  publish(manyPending);
  assert.equal(alerts, 1201, "disconnect and reconnect retain known pending identities beyond the history cap");
  owner.accept({ event: { kind: "ask_request", tabId: "reattached", turnId: "turn:1000", ask: { id: "1" } } });
  assert.equal(alerts, 1201, "wire replay also respects retained pending identities");
  publish([]);
  publish([session(9000)]);
  assert.equal(alerts, 1202, "a new pending request still alerts after the old requests resolve");
  console.log("runtime notifications: pending identities survive bounded history eviction and repeated large snapshots");
} finally { owner.dispose(); }
