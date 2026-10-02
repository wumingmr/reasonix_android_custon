import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import type { WireAsk } from "../lib/types";

const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, Element: dom.window.Element,
  HTMLElement: dom.window.HTMLElement, Node: dom.window.Node, localStorage: dom.window.localStorage,
  IS_REACT_ACT_ENVIRONMENT: true,
});
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { AskCard } = await import("../components/AskCard");
const { LocaleProvider } = await import("../lib/i18n");

const root = createRoot(document.getElementById("root")!);
const ask: WireAsk = {
  id: "1", runtimeEpoch: "runtime-a", turnId: "turn-a",
  questions: [{ id: "q1", prompt: "Choose", options: [{ label: "Option A" }, { label: "Option B" }] }],
};
await act(async () => root.render(<LocaleProvider><AskCard ask={ask} draftScope="tab-a" onAnswer={() => {}} onStop={() => {}} /></LocaleProvider>));

const card = () => document.querySelector<HTMLElement>(".prompt-shelf__card")!;
const collapsed = () => card().classList.contains("prompt-shelf__card--collapsed");
const click = async (element: Element) => {
  await act(async () => { element.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })); });
};

assert.equal(collapsed(), false, "a delivered Ask starts expanded");
await click(document.querySelector(".ask-shelf__custom-indicator")!);
assert.equal(collapsed(), false, "clicking the custom-answer row outside its input keeps the card open");
assert.equal(document.activeElement, document.querySelector(".ask-shelf__custom"), "the row click focuses the custom input");
await click(document.querySelector(".ask-shelf__custom-row")!);
assert.equal(collapsed(), false, "clicking the row's own padding keeps the card open");
console.log("  PASS  the custom-answer row never toggles the card's collapse");

await click(document.querySelector(".prompt-shelf__heading")!);
assert.equal(collapsed(), true, "the card's own chrome still collapses it");
console.log("  PASS  non-interactive card chrome still collapses the Ask");

await act(async () => root.unmount());
dom.window.close();
