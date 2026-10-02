import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { copyFile, mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { selectSession } from "./app-page-actions.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ||= path.join(root, ".pw-browsers");
const { chromium, _electron } = await import("playwright");
const native = process.argv.includes("--electron");
const server = await createServer({ root, server: { host: "127.0.0.1", port: 0, hmr: false }, logLevel: "error" });
await server.listen();
let browser, electronApp, profile;
try {
  let page;
  if (native) {
    profile = await mkdtemp(path.join(tmpdir(), "reasonix-responsive-dock-"));
    const main = path.join(profile, "main.cjs");
    await copyFile(path.join(root, "bench/transcript-layout-electron.cjs"), main);
    const requireElectron = createRequire(path.join(root, "../electron/package.json"));
    electronApp = await _electron.launch({ executablePath: requireElectron("electron"), args: [main],
      env: { ...process.env, REASONIX_LAYOUT_URL: "about:blank" } });
    page = await electronApp.firstWindow();
  } else {
    browser = await chromium.launch({ headless: true });
    page = await browser.newPage();
  }
  const resize = async (width, height) => {
    if (electronApp) await electronApp.evaluate(({ BrowserWindow }, size) => BrowserWindow.getAllWindows()[0].setContentSize(size.width, size.height), { width, height });
    else await page.setViewportSize({ width, height });
  };
  await resize(1600, 1000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.addInitScript(() => localStorage.setItem("reasonix-lang", "en"));
  await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/?mock=bench&bench=1`);
  await page.locator("textarea.composer__input:not([aria-hidden=true])").waitFor();
  await selectSession(page, "bench:small-6t");
  await page.getByRole("tab", { name: "Overview", exact: true }).waitFor();
  const dock = page.locator(".workbench-dock");
  const near = async (width) => {
    try {
      await page.waitForFunction(expected => Math.abs((document.querySelector(".workbench-dock")?.getBoundingClientRect().width ?? 0) - expected) < 2, width);
    } catch (error) {
      console.error(await page.evaluate(() => [".layout", ".chat-pane", ".workbench-dock"].map(selector => {
        const el = document.querySelector(selector);
        return { selector, className: el?.className, rect: el?.getBoundingClientRect().toJSON(), grid: el && getComputedStyle(el).gridTemplateColumns };
      })));
      throw error;
    }
  };
  await near(720);
  const splitter = page.locator(".workspace-panel-resizer");
  await splitter.focus();
  await page.keyboard.press("ArrowLeft");
  await near(736);
  const saved = await page.evaluate(() => localStorage.getItem("reasonix.layoutPreferences.v1"));
  await dock.getByRole("button", { name: "Close workspace panel", exact: true }).click();
  await dock.waitFor({ state: "detached" });
  await page.locator(".topicbar__chrome-btn--workspace[aria-pressed=false]").click();
  await near(736);
  // Browser-only mock has no Electron host/menu entry. Mount the actual browser
  // panel through its tab store; native page sizing has separate host tests.
  await page.evaluate(async () => {
    const { useActivityBarStore } = await import("/src/store/activityBar.ts");
    useActivityBarStore.getState().openEntry("browser", "Browser");
  });
  await page.locator(".browser-panel").waitFor();
  for (const width of [1100, 1024, 1000, 800, 768]) {
    console.log(`checking split layout at ${width}`);
    await resize(width, 900);
    // Read the actual sidebar preference rather than assuming a platform width.
    await page.waitForFunction(w => {
      const chat = document.querySelector(".chat-pane").getBoundingClientRect();
      const dock = document.querySelector(".workbench-dock").getBoundingClientRect();
      return chat.width >= 399 && dock.width >= 299 && dock.right <= w + 1 && chat.right <= dock.left + 1;
    }, width);
    assert(await dock.isVisible(), `dock visible at ${width}`);
    if (width === 800) {
      await page.locator('button[aria-label="Terminal"][aria-pressed]').click();
      await page.waitForFunction(() => {
        const drawer = document.querySelector(".terminal-drawer")?.getBoundingClientRect();
        return drawer && drawer.width >= 399 && drawer.height > 100;
      });
      await page.locator('button[aria-label="Terminal"][aria-pressed]').click();
    }
  }
  for (const width of [767, 600, 390]) {
    console.log(`checking fullscreen at ${width}`);
    await resize(width, 800);
    await near(width);
    assert.equal(await page.locator(".chat-pane").isVisible(), false, "fullscreen removes covered chat controls from focus");
    assert.equal(await splitter.count(), 0, "fullscreen has no resize handle");
    const rect = await dock.boundingBox();
    assert(rect.x >= -1 && rect.x + rect.width <= width + 1);
    await dock.getByRole("button", { name: "Close workspace panel", exact: true }).click();
    assert(await page.locator(".chat-pane").isVisible(), "closing fullscreen returns to chat");
    await page.locator(".topicbar__chrome-btn--workspace[aria-pressed=false]").click();
    await near(width);
  }
  await resize(1600, 1000);
  await near(736);
  assert.equal(await page.evaluate(() => localStorage.getItem("reasonix.layoutPreferences.v1")), saved);
  await page.reload();
  await near(736);
  const handle = await splitter.boundingBox();
  await page.mouse.move(handle.x, handle.y + handle.height / 2);
  await page.mouse.down();
  await page.mouse.move(handle.x - 64, handle.y + handle.height / 2, { steps: 5 });
  await page.mouse.up();
  await near(800);
  assert.deepEqual(errors, []);
  console.log(`responsive dock ${native ? "Electron" : "browser"}: initial ratio, keyboard/pointer resize, Browser tab, narrow fullscreen, close/reopen and restart passed`);
} finally {
  await browser?.close();
  await electronApp?.close();
  await server.close();
  if (profile) await rm(profile, { recursive: true, force: true });
}
