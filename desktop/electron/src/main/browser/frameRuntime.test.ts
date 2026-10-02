import assert from "node:assert/strict";
import { test } from "node:test";
import { FakeFrame, FakePage } from "./fakeGuestViews.js";
import { FrameRuntime, runChildFrame, withFrameOperationSignal } from "./frameRuntime.js";
import { uploadFiles } from "./upload.js";
import type { LocatedRef } from "./refResolver.js";
import { DEBUGGER_IDLE_MS, disposeDebugger } from "./debuggerLease.js";

function fixture() {
  const page = new FakePage(1);
  page.debugger.attached = true;
  const parent = new FakeFrame(2, "https://parent.test", () => undefined);
  const child = new FakeFrame(3, "https://child.test", () => undefined);
  parent.parent = page.mainFrame; page.mainFrame.children.push(parent);
  child.parent = parent; parent.children.push(child);
  const sessions = new Set<string>();
  page.debugger.respond = (method, raw) => {
    const params = raw as Record<string, unknown>;
    if (method === "Page.getFrameTree") return { frameTree: { frame: { id: "root" } } };
    if (method === "Page.createIsolatedWorld") return { executionContextId: params.frameId === "root" ? 1 : params.frameId === "parent" ? 2 : 3 };
    if (method === "Runtime.evaluate") return params.returnByValue ? { result: { value: "observed" } } : { result: { objectId: `owner-${params.contextId}` } };
    if (method === "DOM.describeNode") return { node: { frameId: params.objectId === "owner-1" ? "parent" : "child", backendNodeId: params.objectId === "owner-1" ? 11 : 22 } };
    if (method === "Target.getTargets") return { targetInfos: [{ targetId: "parent", type: "iframe" }, { targetId: "child", type: "iframe" }] };
    if (method === "Target.attachToTarget") { const sessionId = `session-${params.targetId}`; sessions.add(sessionId); return { sessionId }; }
    if (method === "Target.detachFromTarget") sessions.delete(params.sessionId as string);
    return {};
  };
  return { page, parent, child, sessions };
}

test("operations own contexts while adjacent calls reuse the page's target sessions", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const f = fixture(), runtime = new FrameRuntime(f.page);
  assert.equal(await runtime.run(f.child, "read"), "observed");
  assert.equal(await runtime.run(f.parent, "read"), "observed");
  assert.equal(f.page.debugger.commands.filter(c => c.method === "Page.createIsolatedWorld").length, 3);
  assert.equal(f.page.debugger.commands.filter(c => c.method === "Target.detachFromTarget").length, 0, "parents stay available until the operation ends");
  await runtime.close(); await runtime.close();
  assert.equal(f.sessions.size, 2, "targets stay attached across successful adjacent calls");
  assert.equal(await runChildFrame(f.page, f.child, "read"), "observed");
  assert.equal(f.page.debugger.commands.filter(c => c.method === "Target.attachToTarget").length, 2);
  assert.equal(f.page.debugger.commands.filter(c => c.method === "Page.createIsolatedWorld").length, 6, "contexts remain operation-scoped");
  t.mock.timers.tick(DEBUGGER_IDLE_MS);
  assert.deepEqual([...f.sessions], []);
  assert.equal(f.page.debugger.isAttached(), true, "external root attachment is preserved");
});

test("child cleanup failure does not skip releasing the parent session", async () => {
  const f = fixture(), respond = f.page.debugger.respond;
  f.page.debugger.respond = (method, raw) => {
    if (method === "Target.detachFromTarget" && (raw as { sessionId: string }).sessionId === "session-child") throw new Error("target already gone");
    return respond(method, raw);
  };
  assert.equal(await runChildFrame(f.page, f.child, "read"), "observed");
  disposeDebugger(f.page.debugger);
  assert.equal(f.sessions.has("session-parent"), false);
  assert.equal(f.page.debugger.commands.filter(c => c.method === "Target.detachFromTarget").length, 2);
});

test("a failed attachment re-resolves only the retained owner, once, before evaluation", async () => {
  const f = fixture(), respond = f.page.debugger.respond;
  let failed = false;
  f.page.debugger.respond = (method, raw) => {
    if (method === "Target.attachToTarget" && !failed) { failed = true; throw new Error("old target vanished"); }
    if (method === "DOM.describeNode" && failed) return { node: { frameId: "new-parent", backendNodeId: 11 } };
    return respond(method, raw);
  };
  assert.equal(await runChildFrame(f.page, f.parent, "read"), "observed");
  const attachments = f.page.debugger.commands.filter(c => c.method === "Target.attachToTarget").map(c => (c.params as { targetId: string }).targetId);
  assert.deepEqual(attachments, ["parent", "new-parent"]);
  const descriptions = f.page.debugger.commands.filter(c => c.method === "DOM.describeNode").map(c => c.params);
  assert.deepEqual(descriptions, [{ objectId: "owner-1" }, { objectId: "owner-1" }]);
  disposeDebugger(f.page.debugger);
  assert.deepEqual([...f.sessions], []);
});

test("a changed owner or detached native frame never refreshes into another document", async () => {
  for (const change of ["owner", "detached"]) {
    const f = fixture(), respond = f.page.debugger.respond;
    let failed = false;
    f.page.debugger.respond = (method, raw) => {
      if (method === "Target.attachToTarget") { failed = true; if (change === "detached") f.parent.detached = true; throw new Error("old target vanished"); }
      if (method === "DOM.describeNode" && failed) return { node: { frameId: "replacement", backendNodeId: 999 } };
      return respond(method, raw);
    };
    await assert.rejects(runChildFrame(f.page, f.parent, "write-must-not-run"));
    assert.equal(f.page.debugger.commands.filter(c => c.method === "Target.attachToTarget").length, 1);
    assert.equal(f.page.debugger.commands.some(c => c.method === "Runtime.evaluate" && (c.params as { expression: string }).expression === "write-must-not-run"), false);
  }
});

test("connection release waits for operation object cleanup without detaching adjacent sessions", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const f = fixture(), respond = f.page.debugger.respond;
  let entered!: () => void, finish!: () => void;
  const cleaning = new Promise<void>(resolve => { entered = resolve; });
  f.page.debugger.respond = (method, raw) => {
    if (method === "Runtime.releaseObjectGroup") { entered(); return new Promise<void>(resolve => { finish = resolve; }); }
    return respond(method, raw);
  };
  const runtime = new FrameRuntime(f.page);
  await runtime.run(f.child, "read");
  const closing = runtime.close();
  await cleaning;
  assert.equal(f.sessions.has("session-parent"), true);
  assert.equal(f.sessions.has("session-child"), true);
  t.mock.timers.tick(DEBUGGER_IDLE_MS);
  assert.equal(f.sessions.size, 2, "pending object cleanup keeps the connection alive");
  // Complete the parent object release, then allow the root release to settle.
  f.page.debugger.respond = respond;
  finish(); await closing;
  await new Promise<void>(resolve => setImmediate(resolve));
  t.mock.timers.tick(DEBUGGER_IDLE_MS);
  assert.deepEqual([...f.sessions], []);
});

test("deadline rejects an attach and its late result is cleaned without evaluation", async t => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"] });
  const f = fixture(), respond = f.page.debugger.respond;
  let entered!: () => void, attach!: (value: unknown) => void;
  const attaching = new Promise<void>(resolve => { entered = resolve; });
  f.page.debugger.respond = (method, raw) => {
    if (method === "Target.attachToTarget") { entered(); return new Promise(resolve => { attach = resolve; }); }
    return respond(method, raw);
  };
  const running = runChildFrame(f.page, f.parent, "must-not-evaluate");
  const rejected = assert.rejects(running, /deadline exceeded/);
  await attaching;
  t.mock.timers.tick(1000);
  await rejected;
  f.sessions.add("late"); attach({ sessionId: "late" });
  await new Promise<void>(resolve => setImmediate(resolve));
  assert.deepEqual([...f.sessions], []);
  assert.equal(f.page.debugger.commands.some(c => c.method === "Runtime.evaluate" && (c.params as { expression: string }).expression === "must-not-evaluate"), false);
});

test("request cancellation stops frame preparation and consumes a late attachment", async () => {
  const f = fixture(), respond = f.page.debugger.respond, controller = new AbortController();
  let entered!: () => void, attach!: (value: unknown) => void;
  const attaching = new Promise<void>(resolve => { entered = resolve; });
  f.page.debugger.respond = (method, raw) => {
    if (method === "Target.attachToTarget") { entered(); return new Promise(resolve => { attach = resolve; }); }
    return respond(method, raw);
  };
  const running = withFrameOperationSignal(controller.signal, () => runChildFrame(f.page, f.parent, "must-not-evaluate"));
  const rejected = assert.rejects(running, /cancelled|aborted/i);
  await attaching; controller.abort(); await rejected;
  f.sessions.add("late"); attach({ sessionId: "late" });
  await new Promise<void>(resolve => setImmediate(resolve));
  assert.deepEqual([...f.sessions], []);
  assert.equal(f.page.debugger.commands.some(c => c.method === "Runtime.evaluate" && (c.params as { expression: string }).expression === "must-not-evaluate"), false);
});

function uploadFixture() {
  const f = fixture();
  const located: LocatedRef = { frame: f.child, binding: { prefix: "f2", frameTreeNodeId: 3, docId: "document" }, isMainFrame: false, ref: "f2e1", snapshotId: "snapshot", tag: "input", type: "file", path: "" };
  return { ...f, located };
}

test("child upload fences late lookup replies after cancellation, deadline, or frame departure", async t => {
  for (const change of ["cancel", "deadline", "navigate", "detach"] as const) await t.test(change, async t => {
    t.mock.timers.enable({ apis: ["setTimeout", "Date"] });
    const f = uploadFixture(), respond = f.page.debugger.respond, controller = new AbortController();
    let entered!: () => void, finish!: (value: unknown) => void, dispatched = false;
    const looking = new Promise<void>(resolve => { entered = resolve; });
    f.page.debugger.respond = (method, raw) => {
      if (method === "Runtime.callFunctionOn") { entered(); return new Promise(resolve => { finish = resolve; }); }
      return respond(method, raw);
    };
    const running = withFrameOperationSignal(controller.signal, () => uploadFiles(f.page, f.located, ["/tmp/file"], () => {}, () => { dispatched = true; }));
    const rejected = assert.rejects(running, /cancelled|aborted|deadline exceeded|frame navigated/i);
    await looking;
    if (change === "cancel") controller.abort();
    if (change === "deadline") t.mock.timers.tick(1000);
    if (change === "navigate") f.child.url = "https://replacement.test";
    if (change === "detach") f.child.detached = true;
    finish({ result: { objectId: "file-input" } });
    await rejected;
    assert.equal(dispatched, false);
    assert.equal(f.page.debugger.commands.some(c => c.method === "DOM.setFileInputFiles"), false);
    assert.deepEqual([...f.sessions], []);
  });
});

test("child upload deadline releases its sessions while a dispatched write has no receipt", async t => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"] });
  const f = uploadFixture(), respond = f.page.debugger.respond;
  let entered!: () => void, finish!: (value: unknown) => void, dispatches = 0;
  const writing = new Promise<void>(resolve => { entered = resolve; });
  f.page.debugger.respond = (method, raw) => {
    if (method === "Runtime.callFunctionOn") return { result: { objectId: "file-input" } };
    if (method === "DOM.setFileInputFiles") { entered(); return new Promise(resolve => { finish = resolve; }); }
    return respond(method, raw);
  };
  const running = uploadFiles(f.page, f.located, ["/tmp/file"], () => {}, () => { dispatches++; });
  const outcome = running.then(() => "completed", error => String(error));
  await writing;
  t.mock.timers.tick(1000);
  const beforeReply = await Promise.race([outcome, new Promise<string>(resolve => setImmediate(() => resolve("pending")))]);
  const sessionsBeforeReply = [...f.sessions];
  // Always settle the underlying command, including on a failing regression.
  finish({}); await outcome;
  assert.match(beforeReply, /deadline exceeded at DOM.setFileInputFiles/);
  assert.deepEqual(sessionsBeforeReply, [], "owned child sessions are released before the late reply");
  assert.equal(dispatches, 1, "the caller must preserve an unknown write outcome, never replay it");
  assert.equal(f.page.debugger.commands.filter(c => c.method === "DOM.setFileInputFiles").length, 1);
  assert.deepEqual([...f.sessions], []);
});
