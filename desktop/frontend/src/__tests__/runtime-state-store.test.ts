import assert from "node:assert/strict";
import { acceptSessionRuntimeSnapshot, createRuntimeStateStore, selectRuntime, selectRuntimeSession, type RuntimeProjection, type RuntimeState } from "../lib/runtimeStateStore";
import { startRuntimeStateSync } from "../lib/runtimeStateSync";
import { acceptRuntimeState } from "../lib/runtimeStateReducer";

const state: RuntimeState = { schemaVersion: 1, runtimeEpoch: "controller-a", activityRevision: 1, revision: 1, phase: "executing", running: true,
  turnId: "turn-a", turnStatus: "in_progress", turnEventSeq: 1, pendingPrompt: false, cancelRequested: false, cancellable: true, backgroundJobs: 0, activity: "thinking" };
const projection = (revision: number, changes: Partial<RuntimeState> = {}): RuntimeProjection => ({ epoch: "app-a", revision, topics: [],
  sessions: [{ tabId: "a", scope: "project", workspaceRoot: "/fixture", topicId: "topic", sessionPath: "/fixture/session", sessionGeneration: 1,
    open: true, remote: false, freshness: "synced", state: { ...state, revision, ...changes } }] });

{
  const versioned = (projectionEpoch: string, revision: number, todos: RuntimeState["todos"]): RuntimeState => ({
    ...state, projectionEpoch, revision, todos,
  });
  const newest = versioned("producer-a", 20, [{ content: "new", status: "in_progress" }]);
  assert.equal(acceptSessionRuntimeSnapshot(newest, versioned("producer-a", 10, [])), newest,
    "an old empty snapshot cannot clear a newer list");
  const cleared = acceptSessionRuntimeSnapshot(newest, versioned("producer-a", 21, []));
  assert.deepEqual(cleared.todos, [], "a newer empty snapshot is an authoritative clear");
  const replacement = versioned("producer-b", 1, [{ content: "replacement", status: "pending" }]);
  assert.equal(acceptSessionRuntimeSnapshot(cleared, replacement), cleared,
    "an ordinary frame cannot replace the current producer");
  assert.equal(acceptSessionRuntimeSnapshot(cleared, replacement, true), replacement,
    "a binding-validated baseline can establish a replacement producer");
  assert.equal(acceptSessionRuntimeSnapshot(replacement, versioned("producer-a", 99, [])), replacement,
    "the retired producer cannot overwrite its replacement");
}

{
  const frame = projection(1, { todos: [{ content: "A's task", status: "in_progress" }] });
  const session = frame.sessions[0];
  assert.equal(selectRuntimeSession(frame, "a", session.sessionPath), session);
  assert.equal(selectRuntimeSession(frame, "b", session.sessionPath), undefined, "a different tab cannot borrow the runtime");
  assert.equal(selectRuntimeSession(frame, "a", "/fixture/other"), undefined, "reusing a tab cannot borrow the previous session's runtime");
  for (const identity of [undefined, "", {}, { sessionPath: "" }]) {
    assert.equal(selectRuntimeSession(frame, "a", identity), undefined, "unbound and empty sessions never act as wildcards");
  }
  assert.equal(selectRuntimeSession(frame, "a", { sessionPath: session.sessionPath, sessionGeneration: 2 }), undefined, "ABA navigation rejects the old binding generation");
  assert.equal(selectRuntimeSession(frame, "a", { sessionPath: session.sessionPath, sessionGeneration: 1 }), session);
  session.open = false;
  assert.equal(selectRuntimeSession(frame, "a", session.sessionPath), undefined, "detached runtimes are not visible sessions");
  session.open = true;
  session.sessionId = "canonical-a";
  const canonical = { session: { hostId: "local", sessionId: "canonical-a" }, sessionPath: "session-id:canonical-a", sessionGeneration: 1 };
  assert.equal(selectRuntimeSession(frame, "a", canonical), session, "canonical references match even when route and physical path differ");
  assert.equal(selectRuntimeSession(frame, "a", { ...canonical, session: { hostId: "local", sessionId: "other" }, sessionPath: session.sessionPath }), undefined, "matching legacy paths cannot override distinct canonical identities");
  session.remote = true;
  session.hostId = "remote-host";
  assert.equal(selectRuntimeSession(frame, "a", canonical), undefined, "the same session ID on another host is not the same session");
  assert.equal(selectRuntimeSession(frame, "a", { ...canonical, session: { ...canonical.session, hostId: "remote-host" } }), session);
  assert.equal(selectRuntimeSession(frame, "a", session.sessionPath), session, "remote path callers keep their scoped selection");
}
const rawStore = createRuntimeStateStore();
const store = { ...rawStore, accept: (next: RuntimeProjection, authoritative = false) => acceptRuntimeState(rawStore, next, authoritative) };
let updates = 0;
store.subscribe(() => updates++);
assert.equal(store.accept(projection(1)), "accepted");
const first = store.getSnapshot();
assert.equal(store.accept(projection(1)), "duplicate");
assert.equal(store.getSnapshot(), first);
assert.equal(updates, 1);
assert.equal(store.accept(projection(1, { running: false })), "conflict");
assert.equal(store.getSnapshot(), first);
assert.equal(store.accept(projection(0)), "stale");
assert.equal(store.accept(projection(2, { phase: "finishing", activity: "", cancellable: false })), "accepted");
let view = selectRuntime(store.getSnapshot()!.sessions[0]);
assert.equal(view.kind, "finishing"); assert.equal(view.spinning, false); assert.equal(view.cancellable, false); assert.equal(view.running, true);
store.accept(projection(3, { phase: "idle", running: false, activity: "", cancellable: false, backgroundJobs: 2 }));
view = selectRuntime(store.getSnapshot()!.sessions[0]);
assert.equal(view.kind, "background_job"); assert.equal(view.running, false);
store.fail();
assert.equal(selectRuntime(store.getSnapshot()!.sessions[0], store.getFailed()).kind, "unknown");
assert.equal(store.getSnapshot()!.sessions[0].state.backgroundJobs, 2);
assert.equal(store.accept({ ...projection(1), epoch: "other" }), "conflict");
assert.equal(store.accept({ ...projection(1), epoch: "other" }, true), "accepted");
const mutable = projection(10);
assert.equal(store.accept(mutable, true), "accepted");
mutable.sessions[0].state.running = false;
assert.equal(store.getSnapshot()!.sessions[0].state.running, true, "caller mutation cannot change a committed revision");
const malformed = projection(11);
delete (malformed.sessions[0].state as Partial<RuntimeState>).cancellable;
assert.equal(store.accept(malformed), "conflict", "partial new-schema booleans cannot imply idle or uncancellable");

function deferred<T>() { let resolve!: (value: T) => void; let reject!: (err: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
const synced = createRuntimeStateStore();
let receive!: (snapshot: RuntimeProjection) => void, focus!: () => void;
let pending = deferred<RuntimeProjection>();
let reads = 0, subscribed = false, unsubscribed = false;
const timers = new Map<number, { callback: () => void; delay: number }>();
let timerID = 0;
const stop = startRuntimeStateSync({
  subscribe: callback => { subscribed = true; receive = callback; return () => { unsubscribed = true; }; },
  read: () => { assert.equal(subscribed, true, "subscribe before initial GET"); reads++; return pending.promise; },
  timer: (callback, delay) => { const id = ++timerID; timers.set(id, { callback, delay }); return id; },
  clearTimer: id => { timers.delete(id as number); },
  focus: callback => { focus = callback; return () => {}; },
}, synced);
focus(); focus(); assert.equal(reads, 1, "focus shares in-flight GET");
receive(projection(4));
pending.resolve(projection(2));
await pending.promise; await Promise.resolve();
assert.equal(synced.getSnapshot()!.revision, 4, "old GET cannot overwrite SSE");
assert.equal([...timers.values()][0].delay, 30000);
for (const delay of [5000, 10000, 20000, 30000, 30000]) {
  pending = deferred<RuntimeProjection>();
  [...timers.values()][0].callback();
  pending.reject(new Error("offline"));
  await pending.promise.catch(() => {}); await Promise.resolve();
  assert.equal([...timers.values()][0].delay, delay);
  assert.equal(synced.getSnapshot()!.revision, 4, "failure preserves known state");
}
pending = deferred<RuntimeProjection>(); focus(); pending.resolve(projection(5));
await pending.promise; await Promise.resolve();
assert.equal([...timers.values()][0].delay, 30000);
assert.equal(synced.getFailed(), false);
const final = synced.getSnapshot();
stop(); receive(projection(6)); focus();
assert.equal(synced.getSnapshot(), final); assert.equal(unsubscribed, true); assert.equal(timers.size, 0);
console.log("runtime state: immutable revisions, selectors, GET/SSE ordering, recovery, singleflight and disposal passed");

{
  const restored = createRuntimeStateStore();
  let recover!: () => void;
  const replies: Array<(value: RuntimeProjection) => void> = [];
  const dispose = startRuntimeStateSync({
    subscribe: () => () => {},
    recover: callback => { recover = callback; return () => {}; },
    read: () => new Promise(resolve => replies.push(resolve)),
    timer: () => 1,
    clearTimer: () => {},
    focus: () => () => {},
  }, restored);
  recover(); recover();
  assert.equal(restored.getFailed(), true, "a gap marks runtime state unknown until an authoritative read");
  replies.shift()!(projection(99)); await Promise.resolve(); await Promise.resolve();
  assert.equal(restored.getSnapshot(), undefined, "a pre-gap async reply cannot repair the new generation");
  assert.equal(replies.length, 1, "recovery queued during an in-flight read is not lost");
  replies.shift()!({ ...projection(1), epoch: "new-app" }); await Promise.resolve(); await Promise.resolve();
  assert.equal(restored.getSnapshot()?.epoch, "new-app");
  assert.equal(restored.getFailed(), false);
  dispose();
}
