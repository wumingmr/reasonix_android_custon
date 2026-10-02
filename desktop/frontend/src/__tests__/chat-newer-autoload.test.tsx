import assert from "node:assert/strict";
import { act } from "react";
import { createTranscriptHarness } from "./transcript-dom-harness";
import type { Item } from "../lib/useController";

const page: Item[] = [];
for (let turn = 1; turn <= 4; turn += 1) {
  page.push(
    { kind: "user", id: `u${turn}`, text: `prompt ${turn}`, checkpointTurn: turn },
    { kind: "assistant", id: `a${turn}`, text: `answer ${turn}`, reasoning: "", streaming: false },
  );
}
const harness = await createTranscriptHarness({ deterministic: true });
try {
  let loads = 0;
  const window = { hasNewerHistory: true, historyStartTurn: 0, historyEndTurn: 4, totalTurns: 20, geometrySessionKey: "newer-auto" };
  const props = { ...window, onLoadNewerHistory: async () => { loads += 1; return "loaded" as const; } };
  await harness.render(page, props);
  await harness.settle();
  assert.equal(loads, 1, "a reader sitting at the bottom of a window with newer history requests the next page once");
  await harness.render([...page], props);
  await harness.settle();
  assert.equal(loads, 1, "a republished window does not request another page");

  await harness.render(page, { ...props, hasNewerHistory: false });
  await harness.settle();
  assert.equal(loads, 1, "nothing is requested once the window reaches the latest turn");

  let blocked = 0;
  const guarded = { ...window, geometrySessionKey: "newer-auto-selected", onLoadNewerHistory: async () => { blocked += 1; return "loaded" as const; } };
  await harness.render(page, { ...guarded, hasNewerHistory: false });
  await harness.settle();
  const selected = harness.container.querySelector<HTMLElement>('[data-chat-anchor-key="a2"] .md p')!;
  const range = harness.dom.window.document.createRange();
  range.selectNodeContents(selected.firstChild!);
  const selection = harness.dom.window.getSelection()!;
  selection.removeAllRanges();
  selection.addRange(range);
  await harness.render(page, guarded);
  await harness.settle();
  assert.equal(blocked, 0, "a native selection in the transcript blocks the automatic request");

  let failed = 0;
  selection.removeAllRanges();
  const failing = { ...window, geometrySessionKey: "newer-auto-failed", onLoadNewerHistory: async () => { failed += 1; return "empty" as const; } };
  await harness.render(page, failing);
  await harness.settle();
  await harness.render([...page], failing);
  await harness.settle();
  assert.equal(failed, 1, "a page that did not load is not retried without the reader leaving and returning to the bottom");

  const manual = harness.container.querySelector<HTMLButtonElement>(".chat-history-newer .btn")!;
  await act(async () => manual.click());
  assert.equal(failed, 2, "the manual button still requests a page");
  console.log("chat newer auto-load: bottom request, no repeat, selection guard and manual button passed");
} finally { await harness.unmount(); await harness.close(); }
