// Real-page gate for the turn-fork entry: it loads the shipped application
// against the browser dev mock and drives the entry a user clicks, so the
// fixture data, the app wiring, and the notice channel are all exercised
// together. Browsers resolve from the default Playwright cache.
import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startPreviewServer } from "./vite-preview-server.mjs";

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const { chromium } = await import("playwright");
const port = Number(process.env.REASONIX_FORK_APP_PORT ?? 4659);
const server = await startPreviewServer(frontendDir, port);
const browser = await chromium.launch({ headless: true });
const base = `http://127.0.0.1:${port}/`;
const report = { browser: browser.version(), platform: process.platform, arch: process.arch };

const ENTRY = ".chat-actions button.chat-action-icon:not(.copybtn)";
const settle = (page) => page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
const entry = (page) => page.locator(ENTRY).last();
const labels = (page) => page.locator(".workbench-dock__tab-label").allTextContents();

async function openApp(page, query) {
  await page.goto(`${base}${query}`);
  await page.locator("textarea.composer__input:not([aria-hidden=true])").waitFor();
  await page.locator(".chat-column .chat-turn-tail").last().waitFor();
  await settle(page);
}

try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));

  // 1. The dev fixture offers a forkable turn on the page a developer opens.
  await openApp(page, "?mock=1");
  await entry(page).waitFor();
  // The entry exists before the target read lands; wait for it to be enabled and
  // labelled rather than sampling a loading state.
  await page.waitForFunction((selector) => {
    const nodes = document.querySelectorAll(selector);
    const button = nodes[nodes.length - 1];
    return Boolean(button) && button.getAttribute("aria-disabled") === null
      && button.getAttribute("aria-label") === "Branch into a new conversation";
  }, ENTRY, { timeout: 10_000 });
  const label = await entry(page).getAttribute("aria-label");
  assert.equal(await entry(page).getAttribute("aria-disabled"), null, "the running dev app renders an enabled fork entry");
  assert.equal(await entry(page).getAttribute("data-unavailable"), null, "the enabled dev entry is not marked unavailable");
  await entry(page).focus();
  await page.waitForFunction(() => document.querySelector('[role="tooltip"]')?.textContent === "Branch into a new conversation", null, { timeout: 10_000 });
  assert.equal(await page.locator('[role="tooltip"]').textContent(), "Branch into a new conversation", "the dev entry explains itself with the branch label");
  report.devEntry = { label, tooltip: "Branch into a new conversation" };

  // 2. Clicking it creates the child through the shipped controller and adopts its tab.
  const childTitle = () => page.evaluate(() => document.body.innerText.match(/\S[^\n]*\(1\)[^\n]*/)?.[0]?.trim() ?? "");
  assert.equal(await childTitle(), "", "no forked child exists before the click");
  await entry(page).click();
  await page.waitForFunction(() => /\(1\)/.test(document.body.innerText));
  await settle(page);
  const adopted = await childTitle();
  assert.match(adopted, /\(1\)$/, `the created child opens with the mock's fork title (${adopted})`);
  report.adoptedTab = adopted;

  // 3. A child whose tab cannot open surfaces the localized recovery notice, and
  //    a second click on the same turn reuses that child instead of creating another.
  await openApp(page, "?mock=1&fork-attach-failure=1");
  await entry(page).click();
  const warnNotices = () => page.locator('.chat-notice[data-level="warn"]');
  await page.waitForFunction(() => document.querySelectorAll('.chat-notice[data-level="warn"]').length === 1, null, { timeout: 10_000 });
  const first = await warnNotices().last().textContent();
  assert.match(first ?? "", /The branched session mock-fork-/, "the recovery notice names the created child");
  await entry(page).click();
  // The repeated fork reports the child again; wait for the rendered notice
  // instead of assuming one frame is enough.
  await page.waitForFunction(() => document.querySelectorAll('.chat-notice[data-level="warn"]').length === 2, null, { timeout: 10_000 });
  const second = await warnNotices().last().textContent();
  assert.equal(await warnNotices().count(), 2, "a repeated fork reports the child again");
  assert.ok(second?.includes(first?.match(/mock-fork-\S+/)?.[0] ?? "child"), "the repeated fork names the same child, never a second one");
  report.recovery = { first, second };

  await page.screenshot({ path: path.join(process.env.REASONIX_FORK_EVIDENCE ?? "/tmp", "fork-targets-app.png") });
  assert.deepEqual(errors, []);
  console.log(JSON.stringify(report, null, 2));
} finally {
  await browser.close();
  await server.httpServer.close();
}
