import assert from "node:assert/strict";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { createTranscriptHarness } from "./transcript-dom-harness";
import type { Item } from "../lib/useController";

const harness = await createTranscriptHarness({ deterministic: true });
const { ChatSource } = await harness.loadModule<typeof import("../lib/chatViewSource")>("/src/lib/chatViewSource.ts");
const { ChatNodeList } = await harness.loadModule<typeof import("../components/ChatNodes")>("/src/components/ChatNodes.tsx");
const { ChatMountedOrder } = await harness.loadModule<typeof import("../lib/chatMountedOrder")>("/src/lib/chatMountedOrder.ts");
const { ChatContentLoader } = await harness.loadModule<typeof import("../lib/chatContentLoader")>("/src/lib/chatContentLoader.ts");
const { ChatScrollController } = await harness.loadModule<typeof import("../lib/chatScrollController")>("/src/lib/chatScrollController.ts");
const { LocaleProvider } = await harness.loadModule<typeof import("../lib/i18n")>("/src/lib/i18n.tsx");

const source = new ChatSource("reasoning-load-full");
let fail = false;
let release: (() => void) | undefined;
const loads: string[] = [];
const loader = new ChatContentLoader(undefined, (item, field) => {
  if (field !== "reasoning") return Promise.resolve("");
  loads.push(item.id);
  if (fail) return Promise.reject(new Error("unavailable"));
  return new Promise<string>(resolve => { release = () => resolve((item as { reasoning: string }).reasoning); });
});
const scroll = new ChatScrollController("reasoning-load-full");
const mounts = new ChatMountedOrder();
await harness.unmount();
const root = createRoot(harness.container);
const question: Item = { kind: "user", id: "q", text: "think" };
const live = (reasoning: string, streaming = true): Item => ({ kind: "assistant", id: "a", text: "", reasoning, streaming });
const publish = async (items: Item[], running: boolean) => {
  await act(async () => source.update({ items, running, hydrating: false, hasOlder: false, loadingOlder: false }));
  await harness.settle();
};
const body = () => harness.container.querySelector(".chat-reasoning__body")?.textContent ?? "";
const loadButton = () => [...harness.container.querySelectorAll<HTMLButtonElement>(".chat-reasoning__body > .btn")][0];

try {
  const first = "a".repeat(9000);
  await publish([question, live(first)], true);
  await act(async () => root.render(createElement(LocaleProvider, null,
    createElement(ChatNodeList, { source, mounts, loader, scroll, actions: { openDetails: () => {}, recover: () => {} } }))));
  await harness.settle();
  await act(async () => harness.container.querySelector<HTMLElement>(".chat-reasoning [data-disclosure-row]")!.click());
  assert.ok(loadButton(), "long streaming reasoning offers a full-text button");
  await act(async () => loadButton()!.click());
  await harness.settle();
  assert.ok(body().length >= first.length, "clicking load shows the whole reasoning while it streams");
  assert.equal(loadButton(), undefined, "the button is gone once the full text is shown");

  const grown = first + "b".repeat(500);
  await publish([question, live(grown)], true);
  assert.ok(body().length >= grown.length, "the loaded view follows the growing stream instead of snapping back");
  assert.equal(loadButton(), undefined, "streaming chunks do not bring the button back");

  const final = grown + "c".repeat(500);
  await publish([question, live(final, false)], false);
  assert.ok(body().length >= final.length, "the body never shrinks while the settled load is pending");
  assert.equal(loadButton(), undefined, "no button while the settled load is pending");
  await act(async () => { release?.(); });
  await harness.settle();
  assert.ok(body().length >= final.length, "the loaded view survives the settled load");
  assert.equal(loadButton(), undefined, "settling does not bring the button back");
  assert.equal(loads.length, 1, "the settled load runs once");

  const copies = loads.length;
  await publish([question, { ...live("d".repeat(9000)), id: "b" }], true);
  await act(async () => harness.container.querySelectorAll<HTMLButtonElement>(".chat-reasoning .copybtn")[1]?.click());
  assert.equal(loads.length, copies, "copying while streaming does not start a load");

  const start = loads.length;
  await publish([question, { ...live("e".repeat(9000)), id: "c" }], true);
  await act(async () => harness.container.querySelector<HTMLElement>(".chat-reasoning [data-disclosure-row]")!.click());
  await act(async () => loadButton()!.click());
  const idle = loadButton() ? "" : "gone";
  fail = true;
  const failedFinal = "e".repeat(9500);
  await publish([question, { ...live(failedFinal, false), id: "c" }], false);
  await harness.settle();
  assert.equal(loads.length, start + 1, "the failing load ran once");
  assert.ok(body().length >= failedFinal.length, "a failed load does not shrink the body");
  const retry = loadButton();
  assert.ok(retry, `a failed load keeps a retry button (${idle})`);
  assert.equal(retry!.disabled, false, "the retry button is enabled");
  const failedLabel = retry!.textContent;
  fail = false;
  await act(async () => retry!.click());
  await act(async () => { release?.(); });
  await harness.settle();
  assert.equal(loads.length, start + 2, "retry loads again");
  assert.equal(loadButton(), undefined, "a successful retry removes the button");
  assert.ok(failedLabel, "the failed state is labelled");
} finally {
  await act(async () => root.unmount());
  source.dispose(); mounts.dispose(); loader.dispose(); scroll.dispose();
  await harness.unmount(); await harness.close();
}
console.log("reasoning load full: stays loaded across streaming chunks and settlement");
