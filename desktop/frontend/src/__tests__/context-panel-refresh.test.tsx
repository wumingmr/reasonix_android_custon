import assert from "node:assert/strict";
import { mock } from "node:test";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { ContextPanel } from "../components/ContextPanel";
import { LocaleProvider } from "../lib/i18n";
import { installDesktopHostStub } from "./desktopHostStub";
import type { ContextPanelInfo } from "../lib/types";

const dom = new JSDOM('<html><body><div id="root"></div></body></html>', { url: "http://localhost", pretendToBeVisual: true });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, Node: dom.window.Node,
  HTMLElement: dom.window.HTMLElement, Event: dom.window.Event, IS_REACT_ACT_ENVIRONMENT: true,
  ResizeObserver: class { observe() {} unobserve() {} disconnect() {} } });
mock.timers.enable({ apis: ["Date", "setTimeout"], now: 10000 });

function panel(tokens: number): ContextPanelInfo {
  return { usedTokens: 100, windowTokens: 1000, promptTokens: 100, completionTokens: 10,
    totalTokens: tokens, reasoningTokens: 0, cacheHitTokens: 0, cacheMissTokens: 100,
    sessionCacheHitTokens: 0, sessionCacheMissTokens: 100, sessionCompletionTokens: 10,
    requestCount: tokens / 1000, elapsedMs: 1000, sessionCost: tokens / 1000,
    readFiles: [], changedFiles: [] };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(r => { resolve = r; });
  return { promise, resolve };
}
let info = panel(1000);
let calls = 0;
let load = () => Promise.resolve(info);
installDesktopHostStub({ ContextPanel: () => { calls++; return load(); } });
const root = createRoot(document.getElementById("root")!);
let tabId = "tab-a", sessionGen = 1, refreshKey = 0, usageSeq = 0, total = 1000;
async function render() {
  await act(async () => {
    root.render(<LocaleProvider><ContextPanel tabId={tabId} sessionGen={sessionGen}
      refreshKey={refreshKey} usageSeq={usageSeq} sessionTokens={total}
      context={{ used: 100, window: 1000, sessionTokens: total }} /></LocaleProvider>);
  });
}
async function tick(ms: number) { await act(async () => { mock.timers.tick(ms); }); }
const metrics = () => [...document.querySelectorAll(".context-panel__summary-rows .context-panel__mini-stat strong")].map(el => el.textContent);

try {
  await render();
  assert.equal(calls, 1, "mount takes one snapshot");
  await tick(1000);
  info = panel(2000); total = 2000; usageSeq++;
  await render(); await tick(0);
  assert.equal(metrics()[3], "2");
  const beforeBurst = calls;
  await tick(100);
  info = panel(3000); total = 3000; usageSeq++;
  await render();
  assert.equal(metrics()[4], "3,000", "live cumulative total wins before private snapshot refresh");
  assert.equal(calls, beforeBurst, "burst is throttled");
  await tick(900);
  assert.equal(calls, beforeBurst + 1, "last usage schedules a trailing refresh without another event");
  assert.equal(metrics()[3], "3", "request count catches up on trailing refresh");

  // A full refresh inside the bridge coalescing window must fetch fresh data.
  info = panel(4000); total = 4000; refreshKey++;
  await render();
  assert.equal(metrics()[3], "4", "explicit refresh does not reuse the bridge's old 200ms answer");

  // Same-tab session rotation and rapid A -> B -> C navigation, with the old
  // response deliberately arriving last, must not paint the newest session.
  const old = deferred<ContextPanelInfo>();
  load = () => old.promise; refreshKey++;
  await render();
  sessionGen++; total = 0;
  const next = deferred<ContextPanelInfo>(); load = () => next.promise;
  await render();
  assert.equal(metrics()[3], "-", "same-tab rotation immediately hides previous-session metrics");
  const middle = deferred<ContextPanelInfo>(); tabId = "tab-b"; load = () => middle.promise;
  await render();
  tabId = "tab-c"; info = panel(7000); total = 7000; load = () => Promise.resolve(info);
  await render();
  await act(async () => { middle.resolve(panel(6000)); next.resolve(panel(5000)); old.resolve(panel(99000)); });
  assert.equal(metrics()[3], "7", "late old requests cannot overwrite final tab");

  // The last trailing timer must also be cancelled when the panel disappears.
  usageSeq++; await render();
  const beforeUnmount = calls;
  await act(async () => root.unmount());
  await tick(1000);
  assert.equal(calls, beforeUnmount, "unmount cancels pending refresh");
  console.log("context panel refresh: burst, bridge boundary, rotation, rapid switch and unmount passed");
} finally {
  mock.timers.reset();
  dom.window.close();
}
