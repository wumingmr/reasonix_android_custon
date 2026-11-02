import assert from "node:assert/strict";
import React, { act, StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { StudioDiscoveryLifecycle } from "../app-runtime/StudioDiscoveryLifecycle";
import { StudioDiscoveryCard } from "../components/StudioIntroduction";
import { LocaleProvider } from "../lib/i18n";
import { UpdaterProvider } from "../lib/useUpdater";
import { claimStudioIntroduction, isStableOneVersion, studioIntroductionPending, STUDIO_DISCOVERY_KEY, STUDIO_DOWNLOAD_URL } from "../lib/studioDiscovery";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = new JSDOM("<button id='previous'>Previous focus</button><div id='root'></div>", { url: "http://localhost", pretendToBeVisual: true });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement,
  localStorage: dom.window.localStorage, IS_REACT_ACT_ENVIRONMENT: true });
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
window.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList;
const externalOpens: string[] = [];
let version = "v1.39.11";
let needsSetup = false;
const stub = installDesktopHostStub({ Version: async () => version, NeedsOnboarding: async () => needsSetup }, { externalOpens });
const timers = new Map<number, () => void>();
let timerId = 0;
const originalTimer = window.setTimeout;
const originalClear = window.clearTimeout;
window.setTimeout = ((handler: () => void, delay: number) => {
  if (delay !== 1500) return originalTimer(handler, delay);
  const id = ++timerId;
  timers.set(id, handler);
  return id;
}) as typeof window.setTimeout;
window.clearTimeout = (id) => { timers.delete(id); originalClear(id); };
async function tick() {
  await act(async () => {
    const pending = [...timers.values()]; timers.clear(); pending.forEach(fn => fn());
    await new Promise(resolve => setTimeout(resolve, 0));
  });
}
let root = createRoot(document.getElementById("root")!);
async function render(safe = true, stable = true) {
  await act(async () => root.render(<StrictMode><LocaleProvider><UpdaterProvider>
    <StudioDiscoveryLifecycle safeToShow={safe} stableRelease={stable} />
  </UpdaterProvider></LocaleProvider></StrictMode>));
}
async function restart(clear = false) {
  await act(async () => root.unmount());
  if (clear) localStorage.removeItem(STUDIO_DISCOVERY_KEY);
  root = createRoot(document.getElementById("root")!);
}
const dialog = () => document.querySelector('[role="dialog"]');
try {
  for (const value of ["v1.39.11", "1.39.11"]) assert.ok(isStableOneVersion(value));
  for (const value of ["dev", "v1.39.11-beta.1", "2.0.0", "1.39.11+test"]) assert.equal(isStableOneVersion(value), false);
  await render(false); await tick();
  assert.equal(dialog(), null, "busy startup defers promotion without consuming the campaign");
  assert.equal(studioIntroductionPending(), true);
  document.getElementById("previous")!.focus();
  await render(); await tick();
  assert.ok(dialog(), "idle stable native 1.x opens once, including in StrictMode");
  assert.equal(document.activeElement?.textContent, "Continue with 1.x");
  assert.equal(localStorage.getItem(STUDIO_DISCOVERY_KEY), "seen");
  assert.deepEqual(externalOpens, [], "display never starts a download");
  const download = [...document.querySelectorAll("button")].find(button => button.textContent === "Download Studio")!;
  await act(async () => download.click());
  assert.deepEqual(externalOpens, [STUDIO_DOWNLOAD_URL]);
  await act(async () => document.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  assert.equal(dialog(), null);
  assert.equal(document.activeElement?.id, "previous", "close restores focus");
  await restart(); await render(); await tick();
  assert.equal(dialog(), null, "durable marker survives restart");

  await restart(true); await render();
  document.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "a", bubbles: true }));
  await tick(); assert.equal(dialog(), null, "typing wins over promotion");
  assert.equal(studioIntroductionPending(), true);

  await restart(true); await render();
  const overlay = document.createElement("div"); overlay.setAttribute("data-app-overlay", ""); document.body.append(overlay);
  await tick(); assert.equal(dialog(), null, "another overlay is never covered"); overlay.remove();
  assert.equal(studioIntroductionPending(), true);

  for (const scenario of ["setup", "preview", "studio", "capability", "probe-error"]) {
    await restart(true);
    needsSetup = scenario === "setup";
    version = scenario === "preview" ? "v1.39.11-beta.1" : scenario === "studio" ? "v2.0.0" : "v1.39.11";
    stub.commands.Version = scenario === "probe-error" ? async () => { throw new Error("offline"); } : async () => version;
    await render(true, scenario !== "capability"); await tick();
    assert.equal(dialog(), null, `${scenario} skips automatic promotion`);
    assert.equal(studioIntroductionPending(), true);
  }
  await restart(true); needsSetup = false; stub.commands.Version = async () => "v1.39.11";
  await render(); await tick(); assert.ok(dialog());
  await render(false); assert.equal(dialog(), null, "new work or an approval immediately dismisses promotion");

  await restart(true);
  await act(async () => root.render(<LocaleProvider><StudioDiscoveryCard /></LocaleProvider>));
  await act(async () => document.querySelector<HTMLButtonElement>(".studio-discovery button")!.click());
  assert.ok(dialog(), "About can open the introduction independently of startup eligibility");
  assert.equal(studioIntroductionPending(), false, "manual reading consumes the automatic campaign too");
  assert.match(dialog()!.textContent!, /Configuration and history may be shared/);
  await act(async () => [...document.querySelectorAll("button")].find(button => button.textContent === "Continue with 1.x")!.click());
  assert.equal(dialog(), null);

  await restart(true);
  Object.defineProperty(window, "localStorage", { configurable: true, get() { throw new Error("storage unavailable"); } });
  assert.equal(studioIntroductionPending(), false);
  assert.equal(claimStudioIntroduction(), false);
  await render(); await tick(); assert.equal(dialog(), null);
  console.log("PASS Studio introduction: restart, StrictMode, safe startup, focus, download, manual entry and unavailable storage");
} finally {
  await act(async () => root.unmount()); stub.uninstall(); dom.window.close();
}
