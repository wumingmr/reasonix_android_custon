import type { MouseInputEvent } from "electron";
import type { GuestFrame, GuestPage } from "./guestView.js";
import { abortable } from "./captureQueue.js";
import { browserFailure } from "./errors.js";
import { FrameRuntime, frameOperationSignal } from "./frameRuntime.js";
import { scriptCall } from "./pageScripts.js";
import { runInFrame } from "./snapshot.js";

// Chromium's root CDP hit-test graph can still describe the preceding surface
// after native reparenting or emulation changes. Resolve the current frame in
// its isolated DOM world, then send to that frame's owning renderer. In-process
// targets use Electron's direct root-widget input path; OOPIFs use their own
// CDP session, never the root's asynchronous cross-process hit-test routing.
export async function dispatchMouseInput(page: GuestPage, event: MouseInputEvent, inputScale: () => number, verify: () => void = () => {}): Promise<void> {
  const signal = frameOperationSignal();
  const main = page.mainFrame, zoom = page.getZoomFactor(), scale = inputScale();
  const path: Array<{ parent: GuestFrame; frame: GuestFrame; index: number; url: string }> = [];
  let runtime: FrameRuntime | undefined;
  const children = (frame: GuestFrame) => frame.frames ?? frame.framesInSubtree.filter(child => child.parent?.frameTreeNodeId === frame.frameTreeNodeId);
  const checkpoint = () => {
    signal?.throwIfAborted(); verify();
    if (page.isDestroyed() || page.mainFrame !== main || main.detached || page.getZoomFactor() !== zoom || inputScale() !== scale || path.some(item => item.frame.detached || item.frame.url !== item.url || children(item.parent)[item.index] !== item.frame)) throw browserFailure("stale_document", "pointer frame or viewport changed during input preparation");
  };
  const evaluate = async (frame: GuestFrame, method: string, input: unknown) => {
    checkpoint();
    if (frame !== main) runtime ??= new FrameRuntime(page);
    const pending = runInFrame(page, frame, scriptCall(method, input), runtime);
    const result = await (signal ? abortable(pending, signal) : pending);
    checkpoint();
    return result;
  };
  try {
    let frame = main, point = { x: event.x / scale, y: event.y / scale };
    const points = new Map<GuestFrame, { x: number; y: number }>();
    for (;;) {
      points.set(frame, point);
      if (event.type === "mouseMove" && !event.button && await evaluate(frame, "pageInputReady", point) !== true) throw browserFailure("page_not_ready", "input target has not reached a stable rendered frame; observe the page again");
      const child = await evaluate(frame, "pageInputFrame", point) as { index: number; x: number; y: number; unsupported?: boolean } | null;
      if (!child) break;
      if (child.unsupported) throw browserFailure("capability_unsupported", "pointer frame has an unsupported transform");
      if (path.length >= 16) throw browserFailure("capability_unsupported", "pointer frame nesting exceeds the supported depth");
      const next = children(frame)[child.index];
      if (!next || next.detached) throw browserFailure("stale_document", "pointer frame disappeared");
      path.push({ parent: frame, frame: next, index: child.index, url: next.url });
      frame = next; point = child;
    }
    checkpoint();
    if (frame === main) { page.sendInputEvent(event); return; }
    await runtime!.run(frame, "", async (_context, sessionId, send, sessionFrame) => {
      checkpoint();
      if (!sessionId) { page.sendInputEvent(event); return; }
      const ownerPoint = sessionFrame ? points.get(sessionFrame) : undefined;
      if (!ownerPoint) throw browserFailure("stale_document", "pointer no longer belongs to the resolved renderer");
      const buttons = event.type === "mouseUp" ? 0 : event.button === "left" ? 1 : event.button === "right" ? 2 : event.button === "middle" ? 4 : 0;
      // A target session takes CSS pixels local to its renderer. The outer
      // Electron display scale has already been removed during frame traversal;
      // applying it again can hit the same large button at the wrong point.
      await send("Input.dispatchMouseEvent", { type: event.type === "mouseDown" ? "mousePressed" : event.type === "mouseUp" ? "mouseReleased" : "mouseMoved", x: ownerPoint.x, y: ownerPoint.y, button: event.button ?? "none", buttons, clickCount: event.clickCount ?? 0 }, sessionId);
    });
  } catch (error) { await runtime?.close(true); throw error; }
  finally { await runtime?.close(); }
}
