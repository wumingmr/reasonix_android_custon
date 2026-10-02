import assert from "node:assert/strict";
import { test } from "node:test";
import { acquireDebugger, DEBUGGER_IDLE_MS, disposeDebugger, sendDebuggerCommand } from "./debuggerLease.js";
import { FakeDebugger } from "./fakeGuestViews.js";

test("frame, screenshot and upload keep adjacent calls on one root connection", t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const debuggerAPI = new FakeDebugger();
  const first = acquireDebugger(debuggerAPI), second = acquireDebugger(debuggerAPI);
  first(); first();
  assert.equal(debuggerAPI.isAttached(), true);
  second();
  assert.equal(debuggerAPI.isAttached(), true);
  const next = acquireDebugger(debuggerAPI);
  t.mock.timers.tick(DEBUGGER_IDLE_MS);
  assert.equal(debuggerAPI.isAttached(), true);
  next();
  t.mock.timers.tick(DEBUGGER_IDLE_MS);
  assert.equal(debuggerAPI.isAttached(), false);
  debuggerAPI.attach();
  acquireDebugger(debuggerAPI)();
  t.mock.timers.tick(DEBUGGER_IDLE_MS);
  assert.equal(debuggerAPI.isAttached(), true, "externally owned debugger remains attached");
});

test("an abandoned waiter cannot detach a pending command or a replacement lease", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const debuggerAPI = new FakeDebugger();
  let settle!: () => void;
  debuggerAPI.respond = () => new Promise<void>(resolve => { settle = resolve; });
  const release = acquireDebugger(debuggerAPI);
  const command = sendDebuggerCommand(debuggerAPI, "Page.createIsolatedWorld");
  release();
  t.mock.timers.tick(DEBUGGER_IDLE_MS * 2);
  assert.equal(debuggerAPI.isAttached(), true, "a live reply owns the connection");
  disposeDebugger(debuggerAPI);
  const replacement = acquireDebugger(debuggerAPI);
  await assert.rejects(release.send("Input.dispatchMouseEvent"), /lease is unavailable/);
  settle(); await command;
  t.mock.timers.tick(DEBUGGER_IDLE_MS * 2);
  assert.equal(debuggerAPI.isAttached(), true, "old completion cannot detach the replacement");
  replacement(); disposeDebugger(debuggerAPI);
  assert.equal(debuggerAPI.isAttached(), false);
});

test("destroyed notification drops timers without touching a destroyed native debugger", t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const debuggerAPI = new FakeDebugger();
  acquireDebugger(debuggerAPI)();
  debuggerAPI.isAttached = () => { throw new Error("Object has been destroyed"); };
  disposeDebugger(debuggerAPI, false);
  t.mock.timers.tick(DEBUGGER_IDLE_MS * 2);
});
