import assert from "node:assert/strict";
import { initialState, reducer, type State } from "../lib/useController";
import { turnMetrics } from "../lib/turnMetrics";
import { historyMessagesToItems } from "../lib/historyItems";
import type { TranscriptSnapshot } from "../lib/transcriptProtocol";
import type { WireEvent } from "../lib/types";

const originalNow = Date.now;
let now = 426_000;
Date.now = () => now;
const snapshot = (sessionId = "A", content = "x".repeat(52_000)): TranscriptSnapshot => ({
  protocolVersion: 1, snapshotId: `snapshot-${sessionId}`,
  identity: { sessionId, headId: "head", rewriteEpoch: 0, runtimeEpoch: "epoch" },
  projectionRevision: 1, coveredThroughSeq: 10,
  records: [{ id: "m:answer", order: 0, refs: [], message: {
    role: "assistant", messageId: "answer", turnId: `turn-${sessionId}`, content,
  } }], activeRecords: [],
  runtime: { status: "in_progress", turnId: `turn-${sessionId}`, startedAt: 1_000, pendingEvents: [] },
  activeAttempts: [{ id: "attempt", messageId: "answer" }],
  before: 0, hasOlder: false, totalRecords: 1, totalTurns: 1, stale: false,
});
const event = (s: State, e: WireEvent) => reducer(s, { type: "event", e });
const metrics = (s: State) => turnMetrics({ ...s, now, waitAccumMs: 0,
  turnRateOutputQuarters: s.turnRateSample?.outputQuarters })!;
const usage = (completionTokens: number): WireEvent => ({ kind: "usage", usage: {
  promptTokens: 0, completionTokens, totalTokens: completionTokens, cacheHitTokens: 0, cacheMissTokens: 0,
  sessionCacheHitTokens: 0, sessionCacheMissTokens: 0,
} });

try {
  // Both local and remote followers use the same snapshot reducer. Exercise
  // both protocol entry points, including the v2 projected history path.
  for (const remote of [false, true]) for (const v2 of [false, true]) {
    const install = (state: State, snap = snapshot()) => v2
      ? reducer(state, { type: "transcript_v2_snapshot", snapshot: snap, remote,
        projection: { items: historyMessagesToItems(snap.records.map(r => r.message), "snapshot:").items,
          startTurn: 0, endTurn: 1, totalTurns: 1, hasOlder: false, hasNewer: false, revision: 10, revisionKnown: true, digest: "" } })
      : reducer(state, { type: "transcript_snapshot", snapshot: snap, remote });
    now = 426_000;
    let s = install(initialState);
    assert.equal(metrics(s).tokens, 13_000, "restored output remains in the token total");
    assert.equal(metrics(s).tps, null, "a snapshot alone cannot provide throughput");
    s = event(s, { kind: "text", messageId: "answer", text: "x".repeat(40) });
    now += 500;
    assert.equal(metrics(s).tps, 20, "only 10 newly observed tokens belong to the 500ms window");
    assert.equal(metrics(s).elapsedMs, 425_500, "the turn clock retains its original start");
    assert.equal(metrics(s).tokens, 13_010);
    now += 500;
    s = event(s, usage(13_010));
    assert.equal(s.lastRequestTps, 10, "full-request usage cannot inflate the partial observation");
    assert.equal(metrics(s).tps, 10, "usage settlement keeps the matched sample");
    s = event(s, { kind: "message", messageId: "answer", text: "x".repeat(52_040) });

    now += 20_000; // tool execution is outside the provider-output clock
    s = event(s, { kind: "reasoning", messageId: "answer-2", text: "中".repeat(40) });
    now += 1_000;
    assert.equal(metrics(s).tps, 20, "CJK deltas and tool gaps share the same rate window");
    s = event(s, usage(30));
    assert.equal(s.lastRequestTps, 30, "the next request measures only its own output");
    s = event(s, { kind: "turn_done", turnId: "turn-A" });
    assert.equal(metrics(s).tps, 20, "completion does not restore the inflated numerator");
    const answer = s.items.find(item => item.id === "m:answer-2");
    if (!v2) assert.equal(answer?.kind === "assistant" && answer.tokensPerSecond, 20);

    s = event(s, { kind: "turn_started", turnId: "next-turn" });
    assert.equal(s.turnRateSample, undefined, "a new turn returns to ordinary billed telemetry");
    s = install(s, snapshot("B"));
    assert.equal(s.turnDoneAt, 0, "another session does not inherit completion or timing");
    assert.equal(s.turnOutputTokens, 0, "another session does not inherit billed output");
    s = event(s, { kind: "text", messageId: "answer", text: "x".repeat(40) });
    now += 1_000;
    s = install(s, snapshot("B", "x".repeat(100_000)));
    assert.equal(metrics(s).tps, null, "reconnection replaces both sides of the observation window");
    s = event(s, { kind: "text", messageId: "answer", text: "x".repeat(40) });
    now += 1_000;
    assert.equal(metrics(s).tps, 10, "reconnection backlog never becomes newly sampled output");

    // Cumulative tool argument progress needs its own first-observation baseline.
    s = install(initialState);
    s = event(s, { kind: "tool_dispatch", tool: { id: "tool", name: "write_file", readOnly: false, partial: true, argChars: 52_000 } });
    now += 1_000;
    s = event(s, { kind: "tool_dispatch", tool: { id: "tool", name: "write_file", readOnly: false, partial: true, argChars: 52_040 } });
    assert.equal(metrics(s).tps, 10, "restored cumulative arguments count only their increment");
    s = event(s, { kind: "tool_dispatch", tool: { id: "child", parentId: "parent", name: "write_file", readOnly: false, partial: true, argChars: 1_000_000 } });
    assert.equal(metrics(s).tps, 10, "child tool output cannot enter the executor sample");
    s = event(s, { kind: "tool_dispatch", tool: { id: "tool", name: "write_file", readOnly: false, partial: true } });
    assert.equal(metrics(s).tps, 10, "a name-only partial cannot sample a child's cumulative counter");
    s = event(s, { kind: "tool_dispatch", tool: { id: "tool", name: "write_file", readOnly: false, partial: true, argChars: 52_080 } });
    assert.equal(metrics(s).tps, 20, "parent argument progress retains its own baseline");

    s = install(initialState);
    s = event(s, { kind: "text", messageId: "answer", text: "x".repeat(40) });
    now += 1_000;
    s = event(s, { kind: "stream_attempt", messageId: "answer", streamAttempt: { id: "attempt", action: "discard" } });
    now += 20_000;
    s = event(s, { kind: "stream_attempt", messageId: "retry", streamAttempt: { id: "retry", action: "begin" } });
    s = event(s, { kind: "text", messageId: "retry", text: "x".repeat(40) });
    now += 1_000;
    s = event(s, usage(10));
    assert.equal(metrics(s).tps, 10, "retry backoff is outside the sampled provider time");
    assert.equal(s.lastRequestTps, 10, "request settlement pairs all sampled attempts with their intervals");
  }
} finally {
  Date.now = originalNow;
}
console.log("restored turn rate: local/remote snapshots, reconnect, usage, tools and completion passed");
