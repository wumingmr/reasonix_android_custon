import assert from "node:assert/strict";
import { test } from "node:test";
import { FrameSessionPool } from "./frameSessionPool.js";
import { acquireDebugger, disposeDebugger, DEBUGGER_IDLE_MS } from "./debuggerLease.js";
import { FakeDebugger } from "./fakeGuestViews.js";

test("concurrent initialization is shared and one cancellation cannot detach another borrower", async () => {
  const debuggerAPI = new FakeDebugger(); debuggerAPI.attached = true;
  let enter!: () => void, finish!: () => void, nextSession = 0;
  const entered = new Promise<void>(resolve => { enter = resolve; });
  debuggerAPI.respond = method => {
    if (method === "Target.attachToTarget") return { sessionId: `child-${++nextSession}` };
    if (method === "Page.enable") { enter(); return new Promise<void>(resolve => { finish = resolve; }); }
    return {};
  };
  const first = acquireDebugger(debuggerAPI), second = acquireDebugger(debuggerAPI);
  const pool = FrameSessionPool.for(debuggerAPI, first);
  assert.equal(FrameSessionPool.for(debuggerAPI, second), pool);
  const a = pool.borrow("target"), b = pool.borrow("target");
  await entered;
  a.release(true); first();
  assert.equal(debuggerAPI.commands.some(row => row.method === "Target.detachFromTarget"), false);
  finish(); assert.equal(await b.ready, "child-1");
  assert.equal(debuggerAPI.commands.filter(row => row.method === "Page.enable").length, 1);
  b.release(false); second();
  assert.deepEqual(debuggerAPI.commands.filter(row => row.method === "Target.detachFromTarget").map(row => row.params), [{ sessionId: "child-1" }]);
  disposeDebugger(debuggerAPI);
  assert.equal(debuggerAPI.attached, true, "external debugger stays attached");
});

test("an abandoned late attachment cleans only itself and cannot replace a fresh target session", async () => {
  const debuggerAPI = new FakeDebugger(); debuggerAPI.attached = true;
  let finish!: (value: unknown) => void, firstAttach = true;
  debuggerAPI.respond = method => {
    if (method === "Target.attachToTarget") {
      if (firstAttach) { firstAttach = false; return new Promise(resolve => { finish = resolve; }); }
      return { sessionId: "replacement" };
    }
    return {};
  };
  const connection = acquireDebugger(debuggerAPI), pool = FrameSessionPool.for(debuggerAPI, connection);
  const old = pool.borrow("target"), rejected = assert.rejects(old.ready, /invalidated/);
  old.release(true);
  const current = pool.borrow("target");
  assert.equal(await current.ready, "replacement");
  finish({ sessionId: "late" }); await rejected;
  debuggerAPI.emit("Target.detachedFromTarget", { sessionId: "late" });
  const adjacent = pool.borrow("target");
  assert.equal(await adjacent.ready, "replacement");
  assert.equal(debuggerAPI.commands.filter(row => row.method === "Target.attachToTarget").length, 2);
  assert.deepEqual(debuggerAPI.commands.filter(row => row.method === "Target.detachFromTarget").map(row => row.params), [{ sessionId: "late" }]);
  current.release(false); adjacent.release(false); connection(); disposeDebugger(debuggerAPI);
});

test("idle cleanup removes only owned sessions and a detached target is initialized afresh", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const debuggerAPI = new FakeDebugger(); debuggerAPI.attached = true;
  let index = 0;
  debuggerAPI.respond = method => method === "Target.attachToTarget" ? { sessionId: `session-${++index}` } : {};
  const connection = acquireDebugger(debuggerAPI), pool = FrameSessionPool.for(debuggerAPI, connection);
  const first = pool.borrow("target"); await first.ready; first.release(false);
  debuggerAPI.emit("Target.detachedFromTarget", { sessionId: "session-1" });
  const second = pool.borrow("target"); assert.equal(await second.ready, "session-2"); second.release(false);
  connection(); t.mock.timers.tick(DEBUGGER_IDLE_MS);
  assert.deepEqual(debuggerAPI.commands.filter(row => row.method === "Target.detachFromTarget").map(row => row.params), [{ sessionId: "session-2" }]);
  assert.equal(debuggerAPI.attached, true);
  assert.throws(() => pool.borrow("target"), /closed/);
});
