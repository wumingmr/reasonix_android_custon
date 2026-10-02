import assert from "node:assert/strict";
import { act } from "react";
import { installDom, renderComposer } from "./composerInboxHarness";

const dom = installDom();
const originalNow = Date.now;
const now = 426_000;
Date.now = () => now;
let quarters = 40;
const listeners = new Set<() => void>();
const live = { id: "restored", text: "x".repeat(52_040), reasoning: "", reasoningComplete: false };
const { root, rerender } = await renderComposer({
  running: true, tabId: "restored", turnStartAt: now - 425_000,
  turnRateOutputQuarters: 0, turnModelActiveMs: 1_000, turnModelActiveAt: now,
  liveStore: {
    subscribe: (_tab, listener) => { listeners.add(listener); return () => { listeners.delete(listener); }; },
    getSnapshot: () => live,
    getRateOutputQuarters: () => quarters,
  },
});
const rate = () => document.querySelector(".composer-run-strip__metric--optional")?.textContent?.trim();
try {
  assert.equal(rate(), "10 t/s", "restored backlog does not inflate the visible rate");
  await act(async () => { quarters = 80; listeners.forEach(listener => listener()); });
  assert.equal(rate(), "20 t/s", "live-store samples update without a parent render");
  await act(async () => { (document.querySelector(".context-ring") as HTMLButtonElement).click(); });
  const ticker = document.querySelector(".context-ring-popover")?.textContent ?? "";
  assert.ok(ticker.includes("≈20 t/s"), ticker);
  assert.ok(ticker.includes("13K"), "restored output remains in token totals");
  await rerender({ tabId: "other", liveStore: undefined, turnRateOutputQuarters: 0, turnModelActiveMs: 0 });
  assert.equal(rate(), undefined, "another session does not inherit the previous live sample");
  console.log("restored turn rate DOM: strip, popover, live updates and session isolation passed");
} finally {
  await act(async () => { root.unmount(); });
  Date.now = originalNow;
  dom.window.close();
}
