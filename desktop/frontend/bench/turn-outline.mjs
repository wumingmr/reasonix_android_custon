import assert from "node:assert/strict";
import { mkdir, mkdtemp, copyFile, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { chromium, _electron } from "playwright";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const electron = process.argv.includes("--electron");
const evidence = process.env.REASONIX_OUTLINE_EVIDENCE ?? path.join(tmpdir(), "reasonix-turn-outline-evidence");
await mkdir(evidence, { recursive: true });
const host = await mkdtemp(path.join(tmpdir(), "reasonix-outline-host-"));
const server = await createServer({ root, server: { host: "127.0.0.1", port: 0 }, clearScreen: false }); await server.listen();
const url = new URL("bench/turn-outline.html", server.resolvedUrls.local[0]).href;
let browser, app;
const report = { host: electron ? "electron" : "chromium", complete: false, errors: [] };
try {
  let page;
  if (electron) {
    await copyFile(path.join(root, "bench/transcript-layout-electron.cjs"), path.join(host, "main.cjs"));
    app = await _electron.launch({ executablePath: createRequire(path.join(root, "../electron/package.json"))("electron"), args: [path.join(host, "main.cjs")], env: { ...process.env, REASONIX_LAYOUT_URL: url } });
    page = await app.firstWindow();
  } else { browser = await chromium.launch({ headless: true }); page = await browser.newPage({ viewport: { width: 1280, height: 900 } }); await page.goto(url); }
  page.on("pageerror", error => report.errors.push(error.message));
  await page.waitForFunction(() => Boolean(window.turnOutlineFixture));
  const rail = page.locator(".dsh-TurnNavigator-frame"); await rail.waitFor();
  const inspect = () => page.evaluate(() => window.turnOutlineFixture.inspect());
  const go = async key => { await rail.focus(); await rail.press(key); await rail.press("Enter"); };
  await go("Home"); await page.locator('[data-chat-anchor-key="m:u1"]').waitFor();
  const scroller = page.locator(".dsh-TurnNavigator-scroller");
  await scroller.evaluate(el => el.scrollTo({ top: 10000 * 10 }));
  await page.locator('[data-nav-turn="m:u10001"]').waitFor();
  await page.locator('[data-nav-turn="m:u10001"]').focus(); await page.keyboard.press("Enter");
  await page.locator('[data-chat-anchor-key="m:u10001"]').waitFor();
  await go("End"); await page.locator('[data-chat-anchor-key="m:u25000"]').waitFor();
  assert.equal((await inspect()).reads.length, 3);
  assert.ok(await page.locator("[data-nav-turn]").count() <= 52);
  assert.ok((await inspect()).cacheEntries <= 768);
  assert.ok((await inspect()).stateItems <= 96);
  await rail.focus(); await rail.press("Home");
  await page.locator('[data-nav-turn="m:u1"]').waitFor();
  // Pointer input belongs to the rail column; mark buttons are keyboard-only.
  await rail.hover({ position: { x: 14, y: 6 } });
  await page.getByRole("tooltip").filter({ hasText: "Question 1" }).waitFor();
  await page.setViewportSize({ width: 700, height: 800 });
  for (const theme of ["light", "dark"]) {
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
    assert.ok(await page.locator("[data-nav-turn]").count() <= 52);
    await page.screenshot({ path: path.join(evidence, `${report.host}-${theme}-narrow.png`) });
  }
  await page.screenshot({ path: path.join(evidence, `${report.host}-outline.png`) });
  // Native browser wheel input must cancel a delayed target's visible commit.
  await page.evaluate(() => window.turnOutlineFixture.delay()); await go("Home");
  await page.waitForFunction(() => window.turnOutlineFixture.inspect().reads.length === 4);
  await page.locator(".chat-flow-scroll").evaluate(el => { window.outlineWheelReceived = false; el.addEventListener("wheel", () => { window.outlineWheelReceived = true; }, { once: true }); });
  await page.locator(".chat-flow-scroll").hover(); await page.mouse.wheel(0, -80);
  await page.waitForFunction(() => window.outlineWheelReceived === true);
  await page.evaluate(() => window.turnOutlineFixture.resume());
  await page.waitForFunction(() => !document.querySelector('[aria-busy="true"][data-nav-turn]'));
  assert.deepEqual((await inspect()).users, ["u25000"]);
  await page.evaluate(() => window.turnOutlineFixture.delay()); await go("Home");
  await page.waitForFunction(() => window.turnOutlineFixture.inspect().reads.length === 5);
  await page.evaluate(() => window.turnOutlineFixture.switchSession());
  await page.evaluate(() => window.turnOutlineFixture.resume());
  await page.locator('[data-chat-anchor-key="m:u25000"]').waitFor();
  assert.deepEqual((await inspect()).users, ["u25000"]);
  await go("Home");
  await page.locator('[data-chat-anchor-key="m:u1"]').waitFor();
  const beforeLatest = await inspect();
  await page.evaluate(() => window.turnOutlineFixture.delay());
  await page.locator(".chat-to-bottom").click();
  await page.waitForFunction(count => window.turnOutlineFixture.inspect().reads.length === count + 1, beforeLatest.reads.length);
  // A later click on a mounted turn must cancel the pending return-to-latest.
  await go("Home");
  await page.evaluate(() => window.turnOutlineFixture.resume());
  await page.waitForFunction(count => window.turnOutlineFixture.inspect().completedReads === count + 1, beforeLatest.completedReads);
  assert.deepEqual((await inspect()).users, beforeLatest.users);
  assert.equal(await page.locator(".chat-flow-scroll").getAttribute("data-scroll-mode"), "reader");
  await page.locator(".chat-to-bottom").click();
  await page.locator('[data-chat-anchor-key="m:u25000"]').waitFor();
  await page.waitForFunction(() => document.querySelector(".chat-flow-scroll")?.getAttribute("data-scroll-mode") === "tail");
  report.result = await inspect(); report.complete = true; assert.deepEqual(report.errors, []);
} finally {
  await writeFile(path.join(evidence, `${report.host}.json`), JSON.stringify(report, null, 2));
  await app?.close(); await browser?.close(); await server.close(); await rm(host, { recursive: true, force: true });
}
console.log(JSON.stringify(report));
