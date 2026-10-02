import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import { test } from "node:test";

const bundled = build({ entryPoints: [fileURLToPath(new URL("./window.ts", import.meta.url))], bundle: true, platform: "node", format: "cjs", external: ["electron"], write: false });

async function fixture() {
  const windows: FakeWindow[] = [];
  const loads: (() => void)[] = [];
  const scripts: string[] = [];
  class FakeWindow {
    destroyed = false;
    shown = 0;
    bounds: unknown;
    webContents = {
      setWindowOpenHandler() {},
      on() {},
      setZoomFactor() {},
      isDestroyed() { return false; },
      executeJavaScript: async (script: string) => { scripts.push(script); },
    };
    constructor(public options: unknown) { windows.push(this); }
    getNormalBounds() { return { x: 0, y: 0, width: 1280, height: 820 }; }
    on() {}
    setMenuBarVisibility() {}
    setMinimumSize() {}
    setBounds(value: unknown) { this.bounds = value; }
    isDestroyed() { return this.destroyed; }
    destroy() { this.destroyed = true; }
    loadURL() { return new Promise<void>((resolve) => loads.push(resolve)); }
    show() { this.shown++; }
  }
  const display = { workArea: { x: 0, y: 0, width: 1920, height: 1080 } };
  const electron = { BrowserWindow: FakeWindow, nativeTheme: {}, screen: { getPrimaryDisplay: () => display, getDisplayMatching: () => display } };
  const module = { exports: {} as any };
  const require = createRequire(import.meta.url);
  new Function("require", "module", "exports", (await bundled).outputFiles[0].text)((name: string) => name === "electron" ? electron : require(name), module, module.exports);
  const window = new module.exports.MainWindow({ platform: "win32", appURL: "reasonix://app/index.html", preloadPath: "unused", zoomStore: { current: { appZoomFactor: 1 } }, log: { warn() {}, error() {} }, onShellAction() {}, onAppDomReady() {} });
  const geometry = { ...module.exports.DEFAULT_GEOMETRY };
  return { window, windows, loads, scripts, geometry };
}

test("compatible startup window survives transition and applies service geometry", async () => {
  const f = await fixture();
  f.window.create(f.geometry);
  const startup = f.window.showStartup("starting");
  f.loads.shift()!();
  await startup;
  f.window.prepareApp({ ...f.geometry, width: 1000, height: 700 });
  assert.equal(f.windows.length, 1);
  assert.equal(f.windows[0].destroyed, false);
  assert.equal((f.windows[0].bounds as { width: number }).width, 1000);
  assert.equal(f.window.reattachApp(), false, "the real app must still be loaded");
});

test("late startup navigation completion cannot show over the app", async () => {
  const f = await fixture();
  f.window.create(f.geometry);
  const startup = f.window.showStartup("starting");
  f.window.prepareApp(f.geometry);
  const app = f.window.loadApp();
  f.loads.shift()!();
  await startup;
  assert.equal(f.windows[0].shown, 0);
  f.loads.shift()!();
  await app;
});

test("failure replaces startup presentation without accepting its stale completion", async () => {
  const f = await fixture();
  f.window.create(f.geometry);
  const startup = f.window.showStartup("starting");
  const failure = f.window.showFailure("failure");
  f.loads.shift()!();
  await startup;
  assert.equal(f.windows[0].shown, 0);
  f.loads.shift()!();
  await failure;
  assert.equal(f.windows[0].shown, 1);
});

test("incompatible native frame creates the replacement before destroying startup", async () => {
  const f = await fixture();
  f.window.create(f.geometry);
  const startup = f.window.showStartup("starting");
  f.window.prepareApp({ ...f.geometry, frameless: true });
  assert.equal(f.windows.length, 2);
  assert.equal(f.windows[0].destroyed, true);
  f.loads.shift()!();
  await startup;
  assert.equal(f.windows[0].shown, 0);
});

test("app window flushes the renderer draft through a fixed internal script", async () => {
  const f = await fixture();
  f.window.create(f.geometry);
  const app = f.window.loadApp();
  await f.window.flushSessionDraft();
  assert.deepEqual(f.scripts, ["Promise.resolve(globalThis.__reasonixFlushSessionDraft?.())"]);
  f.loads.shift()!();
  await app;
});

test("app window can release the renderer draft exit barrier after a veto", async () => {
  const f = await fixture();
  f.window.create(f.geometry);
  const app = f.window.loadApp();
  await f.window.resumeSessionDraftEditing();
  assert.deepEqual(f.scripts, ["Promise.resolve(globalThis.__reasonixResumeSessionDraftEditing?.())"]);
  f.loads.shift()!();
  await app;
});

test("startup and failure pages do not execute renderer draft hooks", async () => {
  const f = await fixture();
  f.window.create(f.geometry);
  await f.window.flushSessionDraft();
  assert.deepEqual(f.scripts, []);
});
