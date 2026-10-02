// Run: tsx src/__tests__/anchored-popover-scroll.test.tsx

import { JSDOM } from "jsdom";
import React, { useRef } from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { AnchoredPopover } from "../components/AnchoredPopover";

type RectParts = Pick<DOMRect, "left" | "top" | "right" | "bottom" | "width" | "height">;
let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1;
  else failed += 1;
}
function eq(actual: unknown, expected: unknown, label: string) {
  ok(actual === expected, actual === expected ? label : `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}
function rect(parts: RectParts): DOMRect {
  return { ...parts, x: parts.left, y: parts.top, toJSON: () => ({}) } as DOMRect;
}
async function nextFrame() {
  await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => resolve(undefined))); });
}

let closeCount = 0;
function Harness({ showAnchor = true, open = true, hidden = false, visibilityHidden = false, inert = false, zero = false }: {
  showAnchor?: boolean;
  open?: boolean;
  hidden?: boolean;
  visibilityHidden?: boolean;
  inert?: boolean;
  zero?: boolean;
}) {
  const anchorRef = useRef<HTMLButtonElement>(null);
  return (
    <div hidden={hidden || undefined} inert={inert || undefined} style={visibilityHidden ? { visibility: "hidden" } : undefined}>
      {showAnchor && <button ref={anchorRef} data-testid="anchor" data-zero={zero || undefined}>Anchor</button>}
      <AnchoredPopover open={open} anchorRef={anchorRef} onClose={() => { closeCount += 1; }}
        className="test-popover" placement="bottom"><div>Menu</div></AnchoredPopover>
    </div>
  );
}

console.log("\nanchored popover lifecycle and positioning");
const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, Node: dom.window.Node, HTMLElement: dom.window.HTMLElement,
  Event: dom.window.Event, KeyboardEvent: dom.window.KeyboardEvent, MouseEvent: dom.window.MouseEvent,
  requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
  cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
});
Object.defineProperty(window, "innerWidth", { configurable: true, value: 800 });
Object.defineProperty(window, "innerHeight", { configurable: true, value: 600 });

let anchorTop = 100;
let anchorLeft = 60;
let menuHeight = 120;
const originalGetBoundingClientRect = dom.window.HTMLElement.prototype.getBoundingClientRect;
dom.window.HTMLElement.prototype.getBoundingClientRect = function getBoundingClientRect() {
  if (this instanceof dom.window.HTMLElement && this.dataset.testid === "anchor") {
    const width = this.dataset.zero ? 0 : 200;
    const height = this.dataset.zero ? 0 : 30;
    return rect({ left: anchorLeft, top: anchorTop, right: anchorLeft + width, bottom: anchorTop + height, width, height });
  }
  if (this instanceof dom.window.HTMLElement && this.dataset.anchoredPopover === "active") {
    return rect({ left: 0, top: 0, right: 240, bottom: menuHeight, width: 240, height: menuHeight });
  }
  return originalGetBoundingClientRect.call(this);
};

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("missing root");
const root = createRoot(rootEl);

await act(async () => { root.render(<Harness showAnchor={false} />); });
let popover = document.querySelector<HTMLElement>("[data-anchored-popover='active']");
if (!popover) throw new Error("popover did not render during initial measurement");
eq(popover.style.left, "0px", "unmeasured popover does not use a visible corner fallback");
eq(popover.style.top, "0px", "unmeasured popover stays at an inert origin");
eq(popover.style.visibility, "hidden", "unmeasured popover stays hidden");
eq(popover.style.pointerEvents, "none", "unmeasured popover cannot intercept input");
eq(popover.dataset.ready, "false", "unmeasured position does not pretend layout is ready");

await nextFrame();
await act(async () => { root.render(<Harness />); });
await nextFrame();
popover = document.querySelector<HTMLElement>("[data-anchored-popover='active']");
if (!popover) throw new Error("popover did not recover when its anchor mounted");
eq(popover.style.top, "138px", "popover starts below a late-mounted anchor");
eq(popover.dataset.ready, "true", "popover becomes ready after measurement recovers");

anchorTop = 40;
anchorLeft = 90;
await nextFrame();
eq(popover.style.top, "78px", "popover follows anchor movement without an event");
eq(popover.style.left, "90px", "popover follows horizontal layout movement");

anchorTop = 300;
menuHeight = 180;
await nextFrame();
eq(popover.style.top, "338px", "bottom placement follows menu layout-size changes");

await act(async () => { root.render(<Harness showAnchor={false} />); });
await nextFrame();
await nextFrame();
ok(document.querySelector("[data-anchored-popover='active']") === null, "popover is removed when its measured anchor disappears");
eq(closeCount, 1, "anchor invalidation notifies its owner once");
await nextFrame();
eq(closeCount, 1, "stale frames cannot notify the owner again");

await act(async () => { root.render(<Harness open={false} />); });
await act(async () => { root.render(<Harness hidden />); });
ok(document.querySelector("[data-anchored-popover='active']") === null, "hidden ancestor closes before a corner flash");
eq(closeCount, 2, "hidden ancestor clears owner state");

await act(async () => { root.render(<Harness open={false} />); });
await act(async () => { root.render(<Harness inert />); });
ok(document.querySelector("[data-anchored-popover='active']") === null, "inert ancestor closes its portaled popover");
eq(closeCount, 3, "inert ancestor clears owner state");

await act(async () => { root.render(<Harness open={false} />); });
await act(async () => { root.render(<Harness visibilityHidden />); });
ok(document.querySelector("[data-anchored-popover='active']") === null, "visibility-hidden ancestor closes its portaled popover");
eq(closeCount, 4, "visibility-hidden ancestor clears owner state");

await act(async () => { root.render(<Harness open={false} />); });
await act(async () => { root.render(<Harness zero />); });
const zeroPopover = document.querySelector<HTMLElement>("[data-anchored-popover='active']");
eq(zeroPopover?.style.visibility, "hidden", "zero-size anchor stays hidden during bounded retries");
for (let i = 0; i < 9; i += 1) await nextFrame();
ok(document.querySelector("[data-anchored-popover='active']") === null, "zero-size anchor closes after bounded retries");
eq(closeCount, 5, "zero-size anchor clears owner state once");

await act(async () => { root.unmount(); });
dom.window.close();
console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
