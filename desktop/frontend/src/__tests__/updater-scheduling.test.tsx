import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { UpdateBanner } from "../components/UpdateBanner";
import { LocaleProvider } from "../lib/i18n";
import {
  __resetUpdaterCheckScheduleForTests,
  UPDATE_CHECK_INTERVAL_MS,
  UPDATE_CHECK_STORAGE_KEY,
  UpdaterProvider,
  useUpdater,
} from "../lib/useUpdater";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = new JSDOM("<!doctype html><div id='root'></div>", { url: "http://localhost", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  Node: dom.window.Node,
  Element: dom.window.Element,
  HTMLElement: dom.window.HTMLElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
  IS_REACT_ACT_ENVIRONMENT: true,
});

let now = 1_800_000_000_000;
const originalDateNow = Date.now;
Date.now = () => now;

let checkCalls = 0;
const desktopStub = installDesktopHostStub({
  CheckUpdate: async () => {
    checkCalls += 1;
    return {
      available: false,
      current: "v1.2.3",
      latest: "v1.2.3",
      notes: "",
      channel: "stable",
      canSelfUpdate: true,
      manualOnly: false,
      installMode: "portable",
      requiresElevation: false,
      downloaded: false,
      downloadUrl: "https://example.invalid/download",
      assetSize: 0,
    };
  },
});

function ManualCheck() {
  const updater = useUpdater();
  return <button id="manual-check" onClick={() => void updater.check()}>Check</button>;
}

function AutomaticCheck() {
  const updater = useUpdater();
  useEffect(() => { void updater.refresh(); }, [updater.refresh]);
  return null;
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

const root = createRoot(document.getElementById("root")!);
try {
  __resetUpdaterCheckScheduleForTests();
  await act(async () => {
    root.render(<LocaleProvider><UpdaterProvider><UpdateBanner enabled /><ManualCheck /></UpdaterProvider></LocaleProvider>);
    await flush();
  });
  assert.equal(checkCalls, 1, "first enabled mount performs one automatic check");

  now += UPDATE_CHECK_INTERVAL_MS - 1;
  await act(async () => {
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await flush();
  });
  assert.equal(checkCalls, 1, "focus and visibility events stay throttled before six hours");

  now += 1;
  await act(async () => {
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await flush();
  });
  assert.equal(checkCalls, 2, "simultaneous due events produce one check after six hours");

  await act(async () => root.render(<LocaleProvider><UpdaterProvider><AutomaticCheck /><ManualCheck /></UpdaterProvider></LocaleProvider>));
  await act(async () => { await flush(); });
  assert.equal(checkCalls, 2, "stored attempt survives provider remounts");

  await act(async () => {
    (document.getElementById("manual-check") as HTMLButtonElement).click();
    await flush();
  });
  assert.equal(checkCalls, 3, "manual checks bypass the automatic throttle");
  assert.equal(window.localStorage.getItem(UPDATE_CHECK_STORAGE_KEY), String(now), "manual check resets the automatic schedule");

  now += UPDATE_CHECK_INTERVAL_MS - 1;
  await act(async () => {
    window.dispatchEvent(new Event("focus"));
    await flush();
  });
  assert.equal(checkCalls, 3, "manual check suppresses automatic checks for the next six hours");

  await act(async () => root.render(<LocaleProvider><UpdaterProvider><UpdateBanner enabled={false} /></UpdaterProvider></LocaleProvider>));
  now += UPDATE_CHECK_INTERVAL_MS;
  await act(async () => {
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await flush();
  });
  assert.equal(checkCalls, 3, "disabled automatic checks install no startup, focus, or visibility work");

  __resetUpdaterCheckScheduleForTests();
  window.localStorage.setItem(UPDATE_CHECK_STORAGE_KEY, String(now + UPDATE_CHECK_INTERVAL_MS));
  await act(async () => root.render(<LocaleProvider><UpdaterProvider><AutomaticCheck /></UpdaterProvider></LocaleProvider>));
  await act(async () => { await flush(); });
  assert.equal(checkCalls, 4, "a future timestamp is discarded and rebuilt by an immediate check");

  const localStorageDescriptor = Object.getOwnPropertyDescriptor(window, "localStorage");
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    get() { throw new Error("storage denied"); },
  });
  __resetUpdaterCheckScheduleForTests();
  await act(async () => root.render(<LocaleProvider><UpdaterProvider><UpdateBanner enabled /></UpdaterProvider></LocaleProvider>));
  await act(async () => { await flush(); });
  assert.equal(checkCalls, 5, "storage denial still allows the first process-local check");
  await act(async () => {
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await flush();
  });
  assert.equal(checkCalls, 5, "process-local fallback throttles duplicate checks when storage is unavailable");
  if (localStorageDescriptor) Object.defineProperty(window, "localStorage", localStorageDescriptor);

  console.log("PASS updater six-hour scheduling, persistence, manual bypass, disable, and storage recovery");
} finally {
  await act(async () => root.unmount());
  desktopStub.uninstall();
  Date.now = originalDateNow;
  dom.window.close();
}
