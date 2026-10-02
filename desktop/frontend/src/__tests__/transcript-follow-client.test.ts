import assert from "node:assert/strict";
import test from "node:test";
import type { Change, FollowRequest, Snapshot, TranscriptFollowResponse } from "../generated/desktopContract.generated";
import { TranscriptFollowClient, type FollowConsumer, type TranscriptFollowClientOptions } from "../lib/transcriptFollowClient";
import type { TranscriptDiagnostic } from "../lib/transcriptDiagnostics";
import { addBreadcrumb, snapshotBreadcrumbs } from "../lib/breadcrumbs";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}
async function microtasks() { for (let i = 0; i < 16; i++) await Promise.resolve(); }
function baseline(subscription = "subscription", overrides: Partial<Snapshot> = {}): TranscriptFollowResponse {
  return { protocolVersion: 2, subscription, changes: [], resetRequired: false, snapshot: {
    protocolVersion: 1, snapshotId: "cut", identity: { sessionId: "session", headId: "", runtimeEpoch: "epoch", rewriteEpoch: 0 },
    projectionRevision: 10, coveredThroughSeq: 4, durableSeq: 4,
    records: [{ id: "m:answer", order: 0, message: { role: "assistant", messageId: "answer", content: "prefix" }, refs: [] }],
    activeRecords: [], activeAttempts: [], runtime: { status: "in_progress", pendingEvents: [], samplingCount: 0, toolCount: 0 },
    before: 0, hasOlder: false, totalRecords: 1, totalTurns: 1, stale: false, ...overrides,
  } };
}
function suffix(changes: Change[], resetRequired = false): TranscriptFollowResponse {
  return { protocolVersion: 2, subscription: "subscription", changes, resetRequired };
}
function frame(revision: number, text: string): Change {
  return { revision, commitSeq: 4, durableSeq: 4, index: 0, event: { kind: "text", messageId: "answer", text } } as Change;
}
function transport(options: TranscriptFollowClientOptions = {}) {
  const requests: FollowRequest[] = [];
  const pending: ReturnType<typeof deferred<TranscriptFollowResponse>>[] = [];
  const read = (request: FollowRequest) => {
    requests.push(request);
    if (request.close) return Promise.resolve(suffix([]));
    const next = deferred<TranscriptFollowResponse>(); pending.push(next); return next.promise;
  };
  return { requests, pending, client: new TranscriptFollowClient(read, options) };
}
function consumer(): FollowConsumer & { text: string; installed: number; delivered: Change[]; states: string[] } {
  return {
    text: "", installed: 0, delivered: [], states: [],
    install(response) { this.installed++; this.text = response.snapshot!.records[0]?.message.content ?? ""; },
    changes(changes) { this.delivered.push(...changes); for (const change of changes) this.text += change.event?.text ?? ""; },
    connection(state) { this.states.push(state); },
  };
}

test("follow installs the complete baseline before asking for its suffix", async () => {
  const io = transport(); const view = consumer(); const installed = deferred<void>();
  const original = view.install.bind(view);
  view.install = async response => { await installed.promise; await original(response); };
  const starting = io.client.start(view);
  io.pending.shift()!.resolve(baseline());
  await microtasks();
  assert.equal(io.requests.length, 1, "suffix must wait until installation commits");
  installed.resolve(); await starting;
  assert.equal(view.text, "prefix");
  assert.deepEqual(io.requests[1], { subscription: "subscription", afterRevision: 10 });
  io.pending.shift()!.resolve(suffix([frame(11, " suffix")])); await microtasks();
  assert.equal(view.text, "prefix suffix"); io.client.stop();
});

test("follow acknowledges display revisions independently and ignores duplicate frames", async () => {
  const io = transport(); const view = consumer(); const starting = io.client.start(view);
  io.pending.shift()!.resolve(baseline()); await starting;
  io.pending.shift()!.resolve(suffix([frame(11, " first"), frame(11, " first"), frame(12, " second")]));
  await microtasks();
  assert.equal(view.text, "prefix first second"); assert.equal(view.delivered.length, 2);
  assert.deepEqual(io.requests[io.requests.length - 1], { subscription: "subscription", afterRevision: 12 });
  io.pending.shift()!.resolve(suffix([{ revision: 13, firstSeq: 5, commitSeq: 8, durableSeq: 4, index: 0 }]));
  await microtasks(); assert.equal(view.delivered[view.delivered.length - 1]?.commitSeq, 8); io.client.stop();
});

test("settlement pairs with the stable attempt and commit despite delivery order and duplication", async () => {
  const io = transport(); const view = consumer(); const starting = io.client.start(view);
  io.pending.shift()!.resolve(baseline("subscription", { activeAttempts: [{ id: "attempt", messageId: "answer", turnId: "turn", nextIndex: 1 }] }));
  await starting;
  const commit: Change = { revision: 11, firstSeq: 5, commitSeq: 5, durableSeq: 4, index: 0, records: [{ messageId: "answer", role: "assistant", content: "complete" }] };
  const end: Change = { revision: 12, commitSeq: 5, durableSeq: 5, index: 1, attemptId: "attempt", resultSeq: 5, resultKind: "message/complete",
    event: { kind: "stream_attempt", messageId: "answer", streamAttempt: { id: "attempt", action: "commit" } } } as Change;
  io.pending.shift()!.resolve(suffix([end, commit, end])); await microtasks();
  assert.deepEqual(view.delivered.map(change => change.revision), [11, 12]);
  assert.equal(view.states[view.states.length - 1], "connected"); io.client.stop();
});

for (const failure of ["overflow", "revision gap", "business gap", "sampling gap", "settlement mismatch"] as const) {
  test(`follow ${failure} preserves visible content and only requests a new baseline`, async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const diagnostics: TranscriptDiagnostic[] = [];
    const io = transport({ onDiagnostic: (event, visible) => { if (visible) diagnostics.push(event); } });
    const view = consumer(); const starting = io.client.start(view);
    io.pending.shift()!.resolve(baseline()); await starting;
    const broken = failure === "overflow" ? suffix([], true)
      : failure === "revision gap" ? suffix([frame(12, "bad")])
      : failure === "business gap" ? suffix([{ revision: 11, firstSeq: 6, commitSeq: 6, durableSeq: 4, index: 0 }])
      : failure === "settlement mismatch" ? suffix([{ revision: 11, commitSeq: 4, durableSeq: 4, index: 0, attemptId: "unknown", resultSeq: 4, resultKind: "message/complete", event: { kind: "stream_attempt", messageId: "different", streamAttempt: { id: "unknown", action: "commit" } } } as Change])
      : suffix([{ ...frame(11, "bad"), attemptId: "missing-attempt", index: 3 }]);
    io.pending.shift()!.resolve(broken); await microtasks();
    assert.equal(view.text, "prefix"); assert.equal(view.delivered.length, 0);
    assert.equal(view.states[view.states.length - 1], "disconnected");
    assert.ok(io.requests.some(request => request.close && request.subscription === "subscription"));
    const expectedReason = failure === "overflow" ? "resync_required"
      : failure === "revision gap" ? "revision_gap"
      : failure === "business gap" ? "business_gap"
      : failure === "sampling gap" ? "sampling_gap"
      : "settlement_identity_mismatch";
    assert.deepEqual(
      diagnostics[0] && { event: diagnostics[0].event, stage: diagnostics[0].stage, reason: diagnostics[0].reason },
      { event: "failure", stage: "delta_validate", reason: expectedReason },
    );
    t.mock.timers.tick(1000); await microtasks();
    assert.deepEqual(io.requests[io.requests.length - 1], {}, "recovery issues a read, never a model submission");
    assert.equal(view.text, "prefix", "content remains visible while replacement is pending");
    io.pending.shift()!.resolve(baseline("replacement", { projectionRevision: 20, coveredThroughSeq: 8 }));
    await microtasks(); assert.equal(view.installed, 2); assert.equal(view.states[view.states.length - 1], "connected");
    assert.ok(io.requests.every(request => Object.keys(request).every(key => ["subscription", "afterRevision", "close"].includes(key))));
    io.client.stop();
  });
}

test("identical transcript failures are summarized every 30 seconds and recovery is recorded once", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let now = 0;
  const diagnostics: Array<{ event: TranscriptDiagnostic; visible: boolean }> = [];
  const io = transport({ now: () => now, onDiagnostic: (event, visible) => diagnostics.push({ event, visible }) });
  const view = consumer();
  const starting = io.client.start(view);
  io.pending.shift()!.resolve(baseline());
  await starting;

  io.pending.shift()!.reject(new Error("transport unavailable"));
  await microtasks();
  now = 1_000; t.mock.timers.tick(1000); await microtasks();
  io.pending.shift()!.reject(new Error("transport unavailable"));
  await microtasks();
  now = 2_000; t.mock.timers.tick(1000); await microtasks();
  io.pending.shift()!.reject(new Error("transport unavailable"));
  await microtasks();
  now = 32_000; t.mock.timers.tick(1000); await microtasks();
  io.pending.shift()!.reject(new Error("transport unavailable"));
  await microtasks();
  now = 33_000; t.mock.timers.tick(1000); await microtasks();
  io.pending.shift()!.resolve(baseline("replacement", { projectionRevision: 20 }));
  await microtasks();
  assert.ok(!diagnostics.some(({ event }) => event.event === "recovered"), "a baseline alone does not prove following recovered");
  io.pending.shift()!.resolve(suffix([]));
  await microtasks();

  assert.deepEqual(
    diagnostics.map(({ event, visible }) => ({ type: event.event, stage: event.stage, failures: event.failures, visible })),
    [
      { type: "failure", stage: "delta_read", failures: 1, visible: true },
      { type: "failure", stage: "baseline_read", failures: 2, visible: true },
      { type: "failure", stage: "baseline_read", failures: 3, visible: false },
      { type: "summary", stage: "baseline_read", failures: 4, visible: true },
      { type: "recovered", stage: "baseline_read", failures: 4, visible: true },
    ],
  );
  io.client.stop();
});

test("persistent delta failure retains its segment across successful baseline retries", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let now = 0;
  const diagnostics: TranscriptDiagnostic[] = [];
  const io = transport({ now: () => now, onDiagnostic: (event, visible) => { if (visible) diagnostics.push(event); } });
  const starting = io.client.start(consumer());
  io.pending.shift()!.resolve(baseline()); await starting;
  addBreadcrumb("test.close", "preserve navigation and close context");
  for (let i = 0; i < 32; i++) {
    io.pending.shift()!.resolve(suffix([frame(12, "gap")])); await microtasks();
    now += 1000; t.mock.timers.tick(1000); await microtasks();
    io.pending.shift()!.resolve(baseline(`retry-${i}`)); await microtasks();
  }
  assert.deepEqual(diagnostics.map(event => [event.event, event.failures]), [["failure", 1], ["summary", 31]]);
  assert.ok(snapshotBreadcrumbs().some(crumb => crumb.cat === "test.close"));
  io.pending.shift()!.resolve(suffix([frame(11, "valid")])); await microtasks();
  const recovery = diagnostics[diagnostics.length - 1];
  assert.deepEqual([recovery.event, recovery.failures, recovery.durationMs], ["recovered", 32, 32_000]);
  io.client.stop();
});

for (const capability of ["absent", "throws", "rejects"] as const) {
  test(`diagnostic host capability ${capability} cannot interrupt follow recovery`, async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const previous = Object.getOwnPropertyDescriptor(globalThis, "window");
    const native = capability === "absent" ? {} : {
      recordRendererDiagnostic: () => {
        if (capability === "throws") throw new Error("diagnostic transport broken");
        return Promise.reject(new Error("diagnostic write failed"));
      },
    };
    Object.defineProperty(globalThis, "window", { configurable: true, value: { reasonixDesktop: { native } } });
    const io = transport(); const view = consumer();
    try {
      const starting = io.client.start(view);
      io.pending.shift()!.resolve(baseline()); await starting;
      io.pending.shift()!.reject(new Error("original transport failure")); await microtasks();
      t.mock.timers.tick(1000); await microtasks();
      assert.deepEqual(io.requests[io.requests.length - 1], {}, "the original failure still schedules recovery");
      io.pending.shift()!.resolve(baseline("recovered")); await microtasks();
      io.pending.shift()!.resolve(suffix([frame(11, " recovered")])); await microtasks();
      assert.equal(view.text, "prefix recovered");
      assert.equal(view.states[view.states.length - 1], "connected");
    } finally {
      io.client.stop();
      if (previous) Object.defineProperty(globalThis, "window", previous);
      else Reflect.deleteProperty(globalThis, "window");
    }
  });
}

for (const outcome of ["late baseline", "install resolves", "install rejects"] as const) {
  test(`service stopping suppresses cleanup when ${outcome}`, async () => {
    const io = transport(); const view = consumer(); const install = deferred<void>();
    if (outcome !== "late baseline") view.install = () => install.promise;
    const starting = io.client.start(view);
    if (outcome !== "late baseline") { io.pending.shift()!.resolve(baseline()); await microtasks(); }
    io.client.stop(false, "service_stopping");
    if (outcome === "late baseline") io.pending.shift()!.resolve(baseline());
    else if (outcome === "install resolves") install.resolve();
    else install.reject(new Error("late content read rejected"));
    if (outcome === "install rejects") await assert.rejects(starting, /late content read rejected/);
    else await starting;
    assert.ok(!io.requests.some(request => request.close));
    assert.ok(!view.states.includes("connected"));
  });
}

test("remote baseline transport failures retain their channel and stable classification", async () => {
  const diagnostics: TranscriptDiagnostic[] = [];
  const io = transport({ transport: "remote", onDiagnostic: (event, visible) => { if (visible) diagnostics.push(event); } });
  const starting = io.client.start(consumer());
  io.pending.shift()!.reject(new Error("remote response body must stay private"));
  await assert.rejects(starting, /remote response body/);
  assert.deepEqual(
    diagnostics.map(event => ({ transport: event.transport, stage: event.stage, reason: event.reason })),
    [{ transport: "remote", stage: "baseline_read", reason: "transport_rejected" }],
  );
});

test("stopped generations ignore delayed suffixes and close their subscription", async () => {
  const io = transport(); const oldView = consumer(); const starting = io.client.start(oldView);
  io.pending.shift()!.resolve(baseline()); await starting;
  const oldSuffix = io.pending.shift()!; io.client.stop();
  const newView = consumer(); const restarting = io.client.start(newView);
  io.pending.shift()!.resolve(baseline("next", { identity: { sessionId: "next", headId: "", runtimeEpoch: "epoch-next", rewriteEpoch: 0 } }));
  await restarting;
  oldSuffix.resolve(suffix([frame(11, " stale")])); await microtasks();
  assert.equal(oldView.text, "prefix"); assert.equal(newView.text, "prefix");
  assert.ok(io.requests.some(request => request.close && request.subscription === "subscription")); io.client.stop();
});

test("service shutdown stops in-flight delivery without sending a cleanup RPC", async () => {
  const diagnostics: TranscriptDiagnostic[] = [];
  const io = transport({ onDiagnostic: (event, visible) => { if (visible) diagnostics.push(event); } });
  const view = consumer(); const starting = io.client.start(view);
  io.pending.shift()!.resolve(baseline()); await starting;
  const delayed = io.pending.shift()!;
  io.client.stop(false, "service_stopping");
  delayed.resolve(suffix([frame(11, " stale")]));
  await microtasks();

  assert.equal(view.text, "prefix");
  assert.ok(!io.requests.some(request => request.close), "a stopping service must not receive subscription cleanup");
  assert.deepEqual(
    diagnostics.map(event => ({ event: event.event, stage: event.stage, reason: event.reason })),
    [{ event: "stopped", stage: "none", reason: "service_stopping" }],
  );
});

test("stopping before baseline arrives closes the late subscription without installation", async () => {
  const io = transport(); const view = consumer(); const starting = io.client.start(view);
  const late = io.pending.shift()!; io.client.stop(); late.resolve(baseline()); await starting; await microtasks();
  assert.equal(view.installed, 0);
  assert.ok(io.requests.some(request => request.close && request.subscription === "subscription"));
});

test("stopping during asynchronous installation releases the newly opened subscription", async () => {
  const io = transport(); const installed = deferred<void>(); const view = consumer();
  view.install = async () => installed.promise;
  const starting = io.client.start(view); io.pending.shift()!.resolve(baseline()); await microtasks();
  io.client.stop(); installed.resolve(); await starting; await microtasks();
  assert.ok(io.requests.some(request => request.close && request.subscription === "subscription"), "cancelled install leaked its subscription");
  assert.ok(!view.states.includes("connected"));
});

test("a stopped generation's cleanup policy cannot affect a replacement subscription", async () => {
  const io = transport();
  const oldStart = io.client.start(consumer());
  const oldBaseline = io.pending.shift()!;
  io.client.stop(false, "service_stopping");
  const newStart = io.client.start(consumer());
  io.pending.shift()!.resolve(baseline("new-service")); await newStart;
  oldBaseline.resolve(baseline("old-service")); await oldStart;
  io.client.stop();
  assert.deepEqual(io.requests.filter(request => request.close), [{ subscription: "new-service", close: true }]);
});
