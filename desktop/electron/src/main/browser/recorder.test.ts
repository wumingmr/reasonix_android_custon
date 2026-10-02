import assert from "node:assert/strict";
import { test } from "node:test";
import { build } from "esbuild";
import { createRequire } from "node:module";
import { EventEmitter } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { BrowserSurfaceManager } from "./surfaceManager.js";
import { FakeViewFactory, silentLog } from "./fakeGuestViews.js";
import { GrantRegistry } from "./grants.js";
import type { BrowserRecorder } from "./recorder.js";

// Only native allocation is faked. Exercise the production recorder state
// machine with a deterministic barrier before surface preparation completes.
const bundle = await build({ entryPoints: [resolve("src/main/browser/recorder.ts")], bundle: true, platform: "node", format: "cjs", external: ["electron"], write: false, logLevel: "silent" });
const require = createRequire(import.meta.url);
const electron = {
  BrowserWindow: class { constructor() { assert.fail("cancelled preparation allocated a renderer"); } },
  MessageChannelMain: class { port1 = { postMessage() {}, close() {} }; port2 = {}; },
  session: { fromPartition: () => ({ setDisplayMediaRequestHandler() {} }) },
};
const module = { exports: {} as { BrowserRecorder: typeof BrowserRecorder } };
new Function("require", "module", "exports", "__dirname", bundle.outputFiles[0].text)((name: string) => name === "electron" ? electron : require(name), module, module.exports, resolve("dist"));

async function fixture() {
  const directory = await mkdtemp(join(tmpdir(), "recorder-state-"));
  const views = new FakeViewFactory();
  const surfaces = new BrowserSurfaceManager({ views, contentSize: () => null, onTakeover() {}, onCrash() {}, log: silentLog });
  const tab = await surfaces.open("https://example.test", { taskId: "task", sessionId: "session", temporary: true });
  surfaces.takeover(tab.id, "user starts recording");
  let entered!: () => void;
  const preparing = new Promise<void>(resolve => { entered = resolve; });
  tab.view.prepareCapture = signal => { entered(); return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true })); };
  const recorder = new module.exports.BrowserRecorder(surfaces, new GrantRegistry({ generation: () => "g" }), directory);
  const start = recorder.userRequest(tab, "start", directory).then(() => undefined, () => undefined);
  await preparing;
  return { surfaces, tab, recorder, directory, async close() { await recorder.close(); await start; surfaces.destroyAll(); await rm(directory, { recursive: true, force: true }); } };
}

test("user recording survives ordinary input but still stops on page or service loss", async () => {
  for (const reason of ["navigation", "crash", "renderer-loss"]) {
    const f = await fixture();
    try {
      f.surfaces.takeover(f.tab.id, "user keydown");
      assert.equal((await f.recorder.userRequest(f.tab, "status", f.directory))?.state, "preparing", "user input must not cancel a user-owned recording");
      const view = f.tab.view as import("./fakeGuestViews.js").FakeGuestView;
      if (reason === "navigation") view.fire().onNavigate("https://example.test/next", false);
      else if (reason === "crash") view.fire().onRenderProcessGone("crashed");
      else f.surfaces.pauseForRendererLoss("service disconnected");
      await f.recorder.close();
      assert.equal((await f.recorder.userRequest(f.tab, "status", f.directory))?.state, "interrupted");
    } finally { await f.close(); }
  }
});

test("stop during preparation cancels instead of waiting for the recording duration", async () => {
  const f = await fixture();
  try {
    const stop = f.recorder.userRequest(f.tab, "stop", f.directory);
    await stop;
    assert.equal((await f.recorder.userRequest(f.tab, "status", f.directory))?.state, "cancelled");
  } finally { await f.close(); }
});

test("terminal recording status publishes only after cleanup releases the global slot", async () => {
  for (const outcome of ["completed", "cancelled"] as const) {
    const directory = await mkdtemp(join(tmpdir(), "recorder-publication-"));
    await writeFile(join(directory, "browser-recorder.js"), "void 0;");
    let releaseCleanup!: () => void, reachedCleanup!: () => void;
    const cleanupBlocked = new Promise<void>(resolve => { releaseCleanup = resolve; });
    const cleaning = new Promise<void>(resolve => { reachedCleanup = resolve; });
    let port!: EventEmitter & { postMessage(data: { type: string }): void; close(): void; start(): void };
    const fakeElectron = {
      session: electron.session,
      MessageChannelMain: class {
        port1 = port = Object.assign(new EventEmitter(), { close() {}, start() {}, postMessage(data: { type: string }) {
          if (data.type !== "stop") return;
          const bytes = Buffer.alloc(40); bytes.writeUInt32BE(0x1a45dfa3);
          port.emit("message", { data: { type: "chunk", data: bytes } });
          port.emit("message", { data: { type: "stopped", width: 800, height: 600, durationMs: 1000 } });
        } });
        port2 = {};
      },
      BrowserWindow: class {
        webContents = { once() {}, postMessage() { port.emit("message", { data: { type: "started" } }); } };
        async loadFile() {}
        isDestroyed() { return false; }
        destroy() {}
      },
    };
    const loaded = { exports: {} as { BrowserRecorder: typeof BrowserRecorder } };
    const fs = require("node:fs/promises");
    new Function("require", "module", "exports", "__dirname", bundle.outputFiles[0].text)((name: string) => {
      if (name === "electron") return fakeElectron;
      if (name === "node:fs/promises") return { ...fs, async rm(path: string, options: unknown) {
        if (path.endsWith(".html")) { reachedCleanup(); await cleanupBlocked; }
        return fs.rm(path, options);
      } };
      return require(name);
    }, loaded, loaded.exports, resolve("dist"));
    const views = new FakeViewFactory();
    const surfaces = new BrowserSurfaceManager({ views, contentSize: () => null, onTakeover() {}, onCrash() {}, log: silentLog });
    const tab = await surfaces.open("https://example.test", { taskId: "task", sessionId: "session", temporary: true });
    views.views[0].page.run = () => ({ width: 800, height: 600 });
    let leases = 0;
    tab.view.prepareCapture = async () => { leases++; return () => { leases--; }; };
    const grants = new GrantRegistry({ generation: () => "g" });
    grants.install({ grantId: "grant", taskId: "task", sessionId: "session" });
    const recorder = new loaded.exports.BrowserRecorder(surfaces, grants, directory);
    try {
      const initial = await recorder.request(tab, "grant", "start", "", directory);
      const finish = recorder.request(tab, "grant", outcome === "completed" ? "stop" : "cancel", initial.id, directory);
      await cleaning;
      const pending = await recorder.request(tab, "grant", "status", initial.id, directory);
      assert.equal(pending.state, "finalizing");
      assert.equal(pending.path, undefined, "a file is not published while cleanup is pending");
      await assert.rejects(recorder.request(tab, "grant", "start", "", directory), /another recording is active/);
      releaseCleanup();
      assert.equal((await finish).state, outcome);
      assert.equal(leases, 0);
      assert.equal((await recorder.request(tab, "grant", "status", initial.id, directory)).state, outcome);
      assert.equal((await recorder.request(tab, "grant", "start", "", directory)).state, "recording", "terminal status guarantees immediate admission");
    } finally { releaseCleanup(); await recorder.close(); surfaces.destroyAll(); await rm(directory, { recursive: true, force: true }); }
  }
});
