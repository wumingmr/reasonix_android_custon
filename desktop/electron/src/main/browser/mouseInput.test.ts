import assert from "node:assert/strict";
import { test } from "node:test";
import { FakeFrame, FakePage } from "./fakeGuestViews.js";
import { dispatchMouseInput } from "./mouseInput.js";
import { withFrameOperationSignal } from "./frameRuntime.js";
import { disposeDebugger } from "./debuggerLease.js";

function fixture(kind: "main" | "local" | "remote") {
  const page = new FakePage(1);
  const child = new FakeFrame(2, "https://child.test", () => undefined);
  child.parent = page.mainFrame; page.mainFrame.children.push(child);
  page.run = source => source.includes("reasonix:pageInputReady") ? true : kind === "main" ? null : { index: 0, x: 40, y: 20 };
  page.debugger.respond = (method, raw) => {
    const params = raw as Record<string, unknown>;
    if (method === "Page.getFrameTree") return { frameTree: { frame: { id: "root" } } };
    if (method === "Page.createIsolatedWorld") return { executionContextId: params.frameId === "root" ? 1 : 2 };
    if (method === "Runtime.evaluate") return params.returnByValue ? { result: { value: String(params.expression).includes("reasonix:pageInputReady") ? true : null } } : { result: { objectId: "owner" } };
    if (method === "DOM.describeNode") return { node: { frameId: "child", backendNodeId: 10 } };
    if (method === "Target.getTargets") return { targetInfos: kind === "remote" ? [{ targetId: "child", type: "iframe" }] : [] };
    if (method === "Target.attachToTarget") return { sessionId: "child-session" };
    return {};
  };
  const routed: Array<{ sessionId?: string; params: unknown }> = [];
  const send = page.debugger.sendCommand.bind(page.debugger);
  page.debugger.sendCommand = async (method, params, sessionId?: string) => {
    if (method === "Input.dispatchMouseEvent") {
      assert.equal(sessionId, "child-session", "root CDP hit-test routing must never choose the target renderer");
      routed.push({ sessionId, params });
    }
    return send(method, params);
  };
  return { page, child, routed };
}

test("mouse input selects its renderer and applies native zoom and display scale once", async () => {
  for (const kind of ["main", "local", "remote"] as const) for (const [zoom, scale] of [[1.25, 1.25], [1, 0.5], [1, 0.75]]) {
    const f = fixture(kind); f.page.zoom = zoom;
    try {
      for (const type of ["mouseMove", "mouseDown", "mouseUp"] as const) await dispatchMouseInput(f.page, { type, x: 100, y: 120, ...(type === "mouseMove" ? {} : { button: "left" as const, clickCount: 1 }) }, () => scale);
      if (kind === "remote") {
        assert.equal(f.page.inputs.length, 0);
        assert.deepEqual(f.routed.map(row => row.params), [
          { type: "mouseMoved", x: 40, y: 20, button: "none", buttons: 0, clickCount: 0 },
          { type: "mousePressed", x: 40, y: 20, button: "left", buttons: 1, clickCount: 1 },
          { type: "mouseReleased", x: 40, y: 20, button: "left", buttons: 0, clickCount: 1 },
        ]);
        assert.equal(f.page.debugger.commands.filter(row => row.method === "Target.attachToTarget").length, 1, "move, press and release share the owning target session");
        assert.equal(f.page.debugger.commands.filter(row => row.method === "Target.detachFromTarget").length, 0);
      } else {
        assert.equal(f.routed.length, 0);
        assert.deepEqual(f.page.inputs.map(event => "x" in event ? [event.x, event.y] : []), [[100, 120], [100, 120], [100, 120]]);
      }
    } finally { disposeDebugger(f.page.debugger); }
  }
});

test("cancelled render preparation consumes late results without dispatching", async () => {
  const f = fixture("remote"), controller = new AbortController();
  let entered!: () => void, finish!: (value: boolean) => void;
  const started = new Promise<void>(resolve => { entered = resolve; });
  f.page.run = () => { entered(); return new Promise<boolean>(resolve => { finish = resolve; }); };
  const running = withFrameOperationSignal(controller.signal, () => dispatchMouseInput(f.page, { type: "mouseMove", x: 30, y: 40 }, () => 1));
  const rejected = assert.rejects(running, /cancelled/);
  await started; controller.abort(); await rejected;
  finish(true); await new Promise<void>(resolve => setImmediate(resolve));
  assert.equal(f.page.inputs.length, 0); assert.equal(f.routed.length, 0);
  assert.equal(f.page.debugger.commands.length, 0);
});

test("same-process descendants of an OOPIF use their renderer owner's coordinates", async () => {
  const f = fixture("remote"), respond = f.page.debugger.respond;
  const inner = new FakeFrame(3, f.child.url, () => undefined);
  inner.parent = f.child; f.child.children.push(inner);
  f.page.debugger.respond = (method, raw) => {
    const params = raw as Record<string, unknown>;
    if (method === "Page.createIsolatedWorld") return { executionContextId: params.frameId === "root" ? 1 : params.frameId === "child" ? 2 : 3 };
    if (method === "Runtime.evaluate") {
      if (!params.returnByValue) return { result: { objectId: `owner-${params.contextId}` } };
      return { result: { value: params.contextId === 2 ? { index: 0, x: 4, y: 5 } : null } };
    }
    if (method === "DOM.describeNode") return { node: { frameId: params.objectId === "owner-1" ? "child" : "inner", backendNodeId: params.objectId === "owner-1" ? 10 : 11 } };
    return respond(method, raw);
  };
  try {
    await dispatchMouseInput(f.page, { type: "mouseDown", x: 100, y: 120, button: "left" }, () => 0.5);
    assert.equal(f.page.inputs.length, 0);
    assert.deepEqual(f.routed, [{ sessionId: "child-session", params: { type: "mousePressed", x: 40, y: 20, button: "left", buttons: 1, clickCount: 0 } }]);
  } finally { disposeDebugger(f.page.debugger); }
});

test("frame replacement and viewport changes during routing refuse input without fallback", async () => {
  for (const change of ["frame", "zoom", "scale", "grant"] as const) {
    const f = fixture("remote"), respond = f.page.debugger.respond;
    let scale = 1, valid = true;
    f.page.debugger.respond = (method, raw) => {
      if (method === "Runtime.evaluate" && (raw as { returnByValue?: boolean }).returnByValue) {
        if (change === "frame") f.child.detached = true;
        if (change === "zoom") f.page.zoom = 2;
        if (change === "scale") scale = 0.5;
        if (change === "grant") valid = false;
      }
      return respond(method, raw);
    };
    try {
      await assert.rejects(dispatchMouseInput(f.page, { type: "mouseDown", x: 30, y: 40, button: "left" }, () => scale, () => { if (!valid) throw new Error("grant revoked"); }));
      assert.equal(f.page.inputs.length, 0); assert.equal(f.routed.length, 0);
      assert.ok(f.page.debugger.commands.some(row => row.method === "Target.detachFromTarget"));
    } finally { disposeDebugger(f.page.debugger); }
  }
});
