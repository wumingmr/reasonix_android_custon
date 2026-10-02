#!/usr/bin/env node

import assert from "node:assert/strict";
import { copyFile, mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = !process.env.PLAYWRIGHT_BROWSERS_PATH || process.env.PLAYWRIGHT_BROWSERS_PATH === ".pw-browsers"
  ? path.join(frontendDir, ".pw-browsers")
  : process.env.PLAYWRIGHT_BROWSERS_PATH;
const { chromium, _electron } = await import("playwright");
const electronEngine = process.argv.includes("--electron");
const electronHome = electronEngine ? await mkdtemp(path.join(tmpdir(), "reasonix-popover-electron-")) : "";
const server = await createServer({
  root: frontendDir,
  server: { host: "127.0.0.1", port: 0, hmr: false },
  logLevel: "error",
  plugins: [{
    name: "popover-test-favicon",
    configureServer(vite) {
      vite.middlewares.use((req, res, next) => {
        if (req.url !== "/favicon.ico") return next();
        res.statusCode = 204;
        res.end();
      });
    },
  }],
});
await server.listen();

let browser;
let electronApp;
try {
  const address = server.httpServer.address();
  if (!address || typeof address === "string") throw new Error("popover test server did not expose a port");
  const url = `http://127.0.0.1:${address.port}/?mock=demo&bench=1`;
  let page;
  if (electronEngine) {
    const main = path.join(electronHome, "main.cjs");
    await copyFile(path.join(frontendDir, "bench/transcript-layout-electron.cjs"), main);
    const electronRequire = createRequire(path.join(frontendDir, "../electron/package.json"));
    electronApp = await _electron.launch({
      executablePath: electronRequire("electron"), args: [main],
      env: { ...process.env, REASONIX_LAYOUT_URL: url },
    });
    page = await electronApp.firstWindow();
  } else {
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_EXECUTABLE });
    page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  }
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  page.on("console", message => { if (message.type() === "error") errors.push(message.text()); });
  await page.addInitScript(() => localStorage.setItem("reasonix-lang", "en"));
  await page.goto(url);
  await page.locator("#composer-input").waitFor({ state: "visible" });
  await page.evaluate(() => {
    window.__reasonixPopoverCornerSamples = [];
    const sample = () => {
      const menu = document.querySelector(".modelsw__menu");
      if (menu instanceof HTMLElement && getComputedStyle(menu).visibility === "visible") {
        const rect = menu.getBoundingClientRect();
        if (rect.left <= 8.5 && rect.top <= 8.5) window.__reasonixPopoverCornerSamples.push({ left: rect.left, top: rect.top });
      }
      window.__reasonixPopoverFrame = requestAnimationFrame(sample);
    };
    sample();
  });

  const input = page.locator("#composer-input");
  await input.fill("/mock-ask");
  await page.locator(".modelsw__trigger:visible").click();
  await page.locator(".modelsw__menu").waitFor({ state: "visible" });
  await input.press("Enter");
  await page.locator(".prompt-shelf--ask").waitFor({ state: "visible" });
  assert.equal(await page.locator(".modelsw__menu").count(), 0, "decision takeover removes the model menu");
  assert.deepEqual(await page.evaluate(() => window.__reasonixPopoverCornerSamples), [], "model menu never flashes in the top-left corner");

  await page.getByRole("button", { name: "Stop task" }).click();
  await page.locator("#composer-input").waitFor({ state: "visible" });
  assert.equal(await page.locator(".modelsw__menu").count(), 0, "model menu stays closed after the composer returns");
  assert.equal(await page.locator(".modelsw__trigger").getAttribute("aria-expanded"), "false");
  await page.evaluate(() => cancelAnimationFrame(window.__reasonixPopoverFrame));
  assert.deepEqual(errors, []);
  console.log(`PASS ${electronEngine ? "Electron" : "Chromium"} decision takeover closes model popover without corner flash or reopen`);
} finally {
  await browser?.close();
  await electronApp?.close();
  await server.close();
  if (electronHome) await rm(electronHome, { recursive: true, force: true });
}
