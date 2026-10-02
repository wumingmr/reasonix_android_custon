import assert from "node:assert/strict";
import { copyFile, mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { createServer } from "vite";
import path from "node:path";
import { fileURLToPath } from "node:url";
const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = !process.env.PLAYWRIGHT_BROWSERS_PATH || process.env.PLAYWRIGHT_BROWSERS_PATH === ".pw-browsers"
  ? path.join(frontendDir, ".pw-browsers")
  : process.env.PLAYWRIGHT_BROWSERS_PATH;
const { chromium, _electron } = await import("playwright");
const electronEngine = process.argv.includes("--electron");
const electronHome = electronEngine ? await mkdtemp(path.join(tmpdir(), "reasonix-dock-electron-")) : "";

const server = await createServer({ root: frontendDir, server: { host: "127.0.0.1", port: 0, hmr: false }, logLevel: "error", plugins: [{
  name: "dock-navigation-fixture",
  configureServer(server) { server.middlewares.use(async (req, res, next) => {
  if (req.url !== "/dock-navigation-fixture") return next();
  res.setHeader("Content-Type", "text/html");
  res.end(await server.transformIndexHtml(req.url, '<html><head><link rel="icon" href="data:,"></head><body><div id="root"></div><script type="module" src="/bench/dock-navigation-fixture.tsx"></script></body></html>'));
  }); },
}] });
await server.listen();
let browser;
let electronApp;
try {
  const address = server.httpServer.address();
  const url = `http://127.0.0.1:${address.port}/dock-navigation-fixture`;
  let page;
  if (electronEngine) {
    const main = path.join(electronHome, "main.cjs");
    await copyFile(path.join(frontendDir, "bench/transcript-layout-electron.cjs"), main);
    const electronRequire = createRequire(path.join(frontendDir, "../electron/package.json"));
    electronApp = await _electron.launch({
      executablePath: electronRequire("electron"),
      args: [main],
      env: { ...process.env, REASONIX_LAYOUT_URL: url },
    });
    page = await electronApp.firstWindow();
  } else {
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_EXECUTABLE });
    page = await browser.newPage();
  }
  await page.addInitScript(() => localStorage.setItem("reasonix-lang", "en"));
  const errors = [];
  page.on("pageerror", error => { errors.push(error.message); console.error(error.message); });
  page.on("console", message => { if (message.type() === "error") { errors.push(message.text()); console.error(message.text()); } });
  await page.goto(url);
  for (const host of ["local", "remote"]) {
    if (host === "remote") await page.locator("#switch-host").click();
    const card = page.locator(".presented-file");
    await card.locator(".presented-file__main").click();
    await page.getByText(`NAVIGATION CONTENT ${host}.txt`, { exact: false }).first().waitFor();
    for (const action of [/source|源码|原始碼/i, /tree|文件树|檔案樹/i]) {
      await card.locator(".presented-file__more").click();
      await page.getByRole("menuitem", { name: action }).click();
      await page.getByText(`NAVIGATION CONTENT ${host}.txt`, { exact: false }).first().waitFor();
    }
    console.log(`PASS ${electronEngine ? "Electron" : "browser"} ${host}: preview, source, reveal-tree`);
  }
  await page.getByRole("treeitem", { name: "a.txt", exact: true }).click();
  await page.locator(".remote-file-view__toolbar button").click();
  await page.locator(".remote-file-view textarea").fill("DRAFT A");
  await page.locator(".remote-file-view__toolbar button").click();
  await page.getByRole("treeitem", { name: "b.txt", exact: true }).click();
  await page.getByText("NAVIGATION CONTENT b.txt", { exact: false }).first().waitFor();
  await page.locator("#finish-save").click();
  assert.match(await page.locator(".remote-file-view").innerText(), /NAVIGATION CONTENT b.txt/);
  assert.doesNotMatch(await page.locator(".remote-file-view").innerText(), /DRAFT A/);
  console.log(`PASS ${electronEngine ? "Electron" : "browser"} old save receipt cannot overwrite replacement file`);
  assert.deepEqual(errors, []);
} finally {
  await browser?.close();
  await electronApp?.close();
  await server.close();
  if (electronHome) await rm(electronHome, { recursive: true, force: true });
}
