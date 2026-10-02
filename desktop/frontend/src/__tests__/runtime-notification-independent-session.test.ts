import assert from "node:assert/strict";
import { test } from "node:test";
import { createRuntimeNotifications } from "../lib/runtimeNotifications";
import { runtimeStateStore, type RuntimeSession } from "../lib/runtimeStateStore";
import { t } from "../lib/i18n";

test("same turn, prompt and session IDs on different hosts keep independent notification titles and dedupe", () => {
  const sessions: RuntimeSession[] = ["host-a", "host-b"].map(hostId => ({
    hostId, sessionId: "shared-id", tabId: `tab-${hostId}`, scope: "project", workspaceRoot: "/repo",
    topicId: "same-topic", sessionPath: "/same/path", sessionGeneration: 1, open: true,
    remote: true, freshness: "synced", state: {
      schemaVersion: 1, runtimeEpoch: "epoch", activityRevision: 1, revision: 1,
      phase: "executing", running: true, turnId: "same-turn", turnStatus: "running", turnEventSeq: 1,
      pendingPrompt: true, pendingInteractions: [{ requestId: "1", kind: "ask", turnId: "same-turn", runtimeEpoch: "epoch", headId: "head" }],
      cancelRequested: false, cancellable: true, backgroundJobs: 0, activity: "",
    },
  }));
  runtimeStateStore.commit({ epoch: "independent-host-notifications", revision: 1, sessions,
    topics: sessions.map(s => ({ scope: s.scope, workspaceRoot: s.workspaceRoot, node: {
      key: "same-key", kind: "topic", topicId: s.topicId, label: s.hostId!,
      session: { hostId: s.hostId!, sessionId: s.sessionId! },
    } })),
  });
  const titles: string[] = [];
  const owner = createRuntimeNotifications(() => ({ activeTabId: "other", t, showToast(message) { titles.push(message); } }));
  try {
    // Wire first, then snapshot: both hosts alert once, using their own title.
    for (const s of sessions) owner.accept({ event: { kind: "ask_request", tabId: s.tabId, turnId: "same-turn", ask: { id: "1" } } });
    assert.equal(titles.length, 2);
    assert.match(titles[0]!, /host-a/);
    assert.match(titles[1]!, /host-b/);
    owner.start();
    assert.equal(titles.length, 2, "authoritative snapshots do not replay either host notification");
  } finally { owner.dispose(); }
});
