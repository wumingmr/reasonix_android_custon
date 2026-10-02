import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { SettingsPanel } from "../components/SettingsPanel";
import { LocaleProvider } from "../lib/i18n";
import { UpdaterProvider } from "../lib/useUpdater";
import { baseSettings, flushPromises } from "../test-support/settingsTestFixtures";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = new JSDOM("<!doctype html><div id='root'></div>", { url: "http://localhost", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  HTMLElement: dom.window.HTMLElement,
  Node: dom.window.Node,
  Event: dom.window.Event,
  CustomEvent: dom.window.CustomEvent,
  localStorage: dom.window.localStorage,
  sessionStorage: dom.window.sessionStorage,
  IS_REACT_ACT_ENVIRONMENT: true,
});
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
window.matchMedia = () => ({ matches: true, addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList;
window.scrollTo = () => {};

let settings = baseSettings("standard");
let applyCalls = 0;
const desktopStub = installDesktopHostStub({
  Settings: async () => settings,
  Version: async () => "v1.2.3",
  CheckUpdate: async () => ({ available: false, current: "v1.2.3", latest: "v1.2.3", channel: "stable" }),
  ApplyUpdateRequest: async () => { applyCalls += 1; },
  FetchAllProviderModels: async () => ({}),
});

async function renderAbout(root: ReturnType<typeof createRoot>) {
  await act(async () => {
    root.render(
      <LocaleProvider>
        <UpdaterProvider>
          <SettingsPanel initialTab="updates" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} />
        </UpdaterProvider>
      </LocaleProvider>,
    );
    await flushPromises();
  });
  await act(async () => { await flushPromises(); });
}

try {
  const stableRoot = createRoot(document.getElementById("root")!);
  await renderAbout(stableRoot);
  assert.ok(document.querySelector('.settings-page--updates[aria-label="About"]'), "legacy updates route opens the About page");
  assert.ok(document.body.textContent?.includes("Current version: v1.2.3"));
  assert.ok(document.querySelector('[aria-label="Check for updates"]'), "stable build shows manual update check");
  assert.ok(document.body.textContent?.includes("Download from official site"), "stable build shows official download entry");
  assert.ok(document.body.textContent?.includes("Update preferences"), "stable build shows update preferences");
  assert.equal(applyCalls, 0, "available update actions never run before a user click");
  await act(async () => stableRoot.unmount());

  settings = { ...baseSettings("standard"), updaterEnabled: false };
  const testRoot = createRoot(document.getElementById("root")!);
  await renderAbout(testRoot);
  assert.ok(document.querySelector('.settings-page--updates[aria-label="About"]'), "test build keeps the legacy route functional");
  assert.equal(document.querySelector('[aria-label="Check for updates"]'), null, "test build hides manual update check");
  assert.equal(document.body.textContent?.includes("Download from official site"), false, "test build hides production download entry");
  assert.equal(document.body.textContent?.includes("Update preferences"), false, "test build hides updater preferences");
  assert.ok(document.body.textContent?.includes("Build identity"), "test build shows build identity");
  assert.ok(document.body.textContent?.includes("Privacy & configuration"), "test build keeps privacy and configuration");
  assert.ok(document.body.textContent?.includes("Release notes"), "test build keeps changelog access");
  assert.ok(document.body.textContent?.includes("Help & feedback"), "test build keeps feedback access");
  await act(async () => testRoot.unmount());

  console.log("PASS About route and stable/test updater capability controls");
} finally {
  desktopStub.uninstall();
  dom.window.close();
}
