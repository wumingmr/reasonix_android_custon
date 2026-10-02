// Run: tsx src/__tests__/anchored-overlay-lifecycle.test.tsx

import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { ComposerChoice } from "../components/ComposerChoice";
import { Tooltip } from "../components/Tooltip";

const dom = new JSDOM('<div id="root"></div>', { pretendToBeVisual: true, url: "http://localhost/" });
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, Node: dom.window.Node,
  MouseEvent: dom.window.MouseEvent, KeyboardEvent: dom.window.KeyboardEvent,
  IS_REACT_ACT_ENVIRONMENT: true,
});
window.matchMedia = (() => ({ matches: true, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
let nextFrameId = 1;
const pendingFrames = new Map<number, FrameRequestCallback>();
const requestFrame = ((callback: FrameRequestCallback) => {
  const id = nextFrameId++;
  pendingFrames.set(id, callback);
  return id;
}) as typeof window.requestAnimationFrame;
const cancelFrame = ((id: number) => pendingFrames.delete(id)) as typeof window.cancelAnimationFrame;
window.requestAnimationFrame = requestFrame;
window.cancelAnimationFrame = cancelFrame;
Object.assign(globalThis, { requestAnimationFrame: requestFrame, cancelAnimationFrame: cancelFrame });
const originalRect = dom.window.HTMLElement.prototype.getBoundingClientRect;
let tooltipHasLayout = true;
dom.window.HTMLElement.prototype.getBoundingClientRect = function getBoundingClientRect() {
  if (this.matches("button, .tooltip-trigger")) return new dom.window.DOMRect(300, 500, 160, 32);
  if (this.matches("[data-anchored-popover]")) return new dom.window.DOMRect(0, 0, 240, 100);
  if (this.matches("[role=tooltip]")) return tooltipHasLayout
    ? new dom.window.DOMRect(0, 0, 80, 28)
    : new dom.window.DOMRect(0, 0, 0, 0);
  return originalRect.call(this);
};
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 1));
  });
  await act(async () => {
    const callbacks = [...pendingFrames.values()];
    pendingFrames.clear();
    callbacks.forEach((callback) => callback(performance.now()));
  });
}

const root = createRoot(document.getElementById("root")!);
async function renderChoice(disabled: boolean, dismissSignal = 0) {
  await act(async () => root.render(<ComposerChoice label="Effort" value="a" disabled={disabled}
    dismissSignal={dismissSignal}
    options={[{ value: "a", label: "A" }, { value: "b", label: "B" }]} onPick={() => {}} />));
}
await renderChoice(false);
await act(async () => document.querySelector<HTMLButtonElement>(".composer-choice")!.click());
assert.ok(document.querySelector("[data-anchored-popover]"), "choice opens its menu");
await renderChoice(true);
await settle();
assert.equal(document.querySelector("[data-anchored-popover]"), null, "disabled closes the menu");
await renderChoice(false);
assert.equal(document.querySelector("[data-anchored-popover]"), null, "disabled then enabled does not reopen the menu");
assert.equal(document.querySelector(".composer-choice")?.getAttribute("aria-expanded"), "false");

await act(async () => document.querySelector<HTMLButtonElement>(".composer-choice")!.click());
assert.ok(document.querySelector("[data-anchored-popover]"), "choice reopens after becoming enabled");
await renderChoice(false, 1);
await settle();
assert.equal(document.querySelector("[data-anchored-popover]"), null, "dismiss signal closes the choice menu");
await renderChoice(false, 2);
assert.equal(document.querySelector("[data-anchored-popover]"), null, "a later dismiss signal does not reopen the menu");

await act(async () => root.render(<div id="tooltip-host"><Tooltip label="Help" delay={0}><button>Info</button></Tooltip></div>));
const trigger = document.querySelector<HTMLElement>(".tooltip-trigger")!;
await act(async () => trigger.dispatchEvent(new dom.window.MouseEvent("mouseover", { bubbles: true })));
await settle();
assert.ok(document.querySelector("[role=tooltip]"), "tooltip opens from a valid anchor");
await act(async () => { (document.getElementById("tooltip-host") as HTMLElement).hidden = true; });
await settle();
assert.equal(document.querySelector("[role=tooltip]"), null, "tooltip closes when its anchor becomes hidden");

await act(async () => {
  (document.getElementById("tooltip-host") as HTMLElement).hidden = false;
  trigger.dispatchEvent(new dom.window.MouseEvent("mouseover", { bubbles: true }));
  (document.getElementById("tooltip-host") as HTMLElement).hidden = true;
});
await settle();
assert.equal(document.querySelector("[role=tooltip]"), null, "hidden anchor cancels delayed tooltip display");

await act(async () => { (document.getElementById("tooltip-host") as HTMLElement).hidden = false; });
tooltipHasLayout = false;
await act(async () => trigger.dispatchEvent(new dom.window.MouseEvent("mouseover", { bubbles: true })));
await settle();
assert.equal((document.querySelector<HTMLElement>("[role=tooltip]")?.style.visibility), "hidden", "unmeasured tooltip stays hidden");
tooltipHasLayout = true;
await settle();
assert.equal((document.querySelector<HTMLElement>("[role=tooltip]")?.style.visibility), "visible", "tooltip becomes visible after delayed layout");

await act(async () => root.unmount());
dom.window.close();
console.log("PASS anchored overlay owner state and tooltip lifecycle");
