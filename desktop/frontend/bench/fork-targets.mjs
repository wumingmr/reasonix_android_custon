// Browser gate for the transcript's turn-fork entry. It builds the bench page
// with the real Transcript and drives it in Chromium, reading the rendered
// states, labels, and notices out of the DOM.
import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { build, preview, loadConfigFromFile } from "vite";

// Browsers resolve from the default Playwright cache; this gate never pins a
// worktree-local path.
const { chromium } = await import("playwright");
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const outDir = await mkdtemp(path.join(tmpdir(), "reasonix-fork-build-"));
const evidence = process.env.REASONIX_FORK_EVIDENCE ?? path.join(tmpdir(), "reasonix-fork-evidence");
await mkdir(evidence, { recursive: true });
const loaded = await loadConfigFromFile({ command: "build", mode: "production" }, path.join(root, "vite.config.ts"));
const config = loaded.config;
await build({ ...config, configFile: false, root, logLevel: "error",
  plugins: config.plugins.filter(plugin => !["archive-hidden-sourcemaps", "keep-dist-placeholder"].includes(plugin?.name)),
  build: { ...config.build, outDir, sourcemap: false,
    rolldownOptions: { ...config.build.rolldownOptions, input: path.join(root, "bench/fork-targets.html") } } });
const server = await preview({ configFile: false, root, logLevel: "error", build: { outDir }, preview: { host: "127.0.0.1", port: 0 } });
const address = server.httpServer.address();
const base = `http://127.0.0.1:${address.port}/bench/fork-targets.html`;
const browser = await chromium.launch({ headless: true });
const report = { browser: browser.version(), platform: process.platform, arch: process.arch, states: {}, clicks: [], notices: [] };

// Selectors: the transcript's action row and the branch entry are the shipped
// ones; the bench adds only the fixture control surface.
const ENTRY = ".chat-actions button.chat-action-icon:not(.copybtn)";
const entry = page => page.locator(ENTRY).last();
const settle = page => page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
const described = (page, selector) => page.evaluate((sel) => {
  // The rendered entry is the LAST turn's, matching the locator used above.
  const button = Array.from(document.querySelectorAll(sel)).at(-1);
  const id = button?.getAttribute("aria-describedby");
  return id ? document.getElementById(id)?.textContent ?? "" : "";
}, selector);
const state = async (page, name) => {
  const button = entry(page);
  await button.waitFor();
  return { name, disabled: await button.getAttribute("aria-disabled"), unavailable: await button.getAttribute("data-unavailable"),
    label: await button.getAttribute("aria-label"), described: await described(page, ENTRY) };
};
// Locale dictionaries load on demand, and until the requested one arrives the
// provider renders its English fallback. Every read of localized copy therefore
// waits for the text itself, so a green run never depends on load timing.
const waitForLabel = (page, expected) => page.waitForFunction(({ selector, expected }) => {
  const nodes = document.querySelectorAll(selector);
  return nodes.length > 0 && nodes[nodes.length - 1].getAttribute("aria-label") === expected;
}, { selector: ENTRY, expected }, { timeout: 10_000 });
const waitForDescribed = (page, expected) => page.waitForFunction(({ selector, expected }) => {
  const nodes = document.querySelectorAll(selector);
  const id = nodes.length ? nodes[nodes.length - 1].getAttribute("aria-describedby") : null;
  return Boolean(id) && document.getElementById(id)?.textContent === expected;
}, { selector: ENTRY, expected }, { timeout: 10_000 });

try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(base);
  await page.locator(".chat-column .chat-turn-tail").last().waitFor();
  await settle(page);

  // A completed turn offers the persisted boundary of its answer.
  await waitForLabel(page, "Branch into a new conversation");
  const completed = await state(page, "completed");
  await entry(page).focus();
  await page.waitForFunction((expected) => (document.querySelector('[role="tooltip"]')?.textContent ?? "") === expected,
    "Branch into a new conversation", { timeout: 10_000 });
  const tooltip = await page.locator('[role="tooltip"]').textContent();
  assert.equal(completed.disabled, null, "a completed turn renders an enabled fork entry");
  assert.equal(completed.unavailable, null, "the enabled entry is not marked unavailable");
  assert.equal(tooltip, completed.label, "the focused entry explains itself with the branch label");
  report.states.completed = { ...completed, tooltip };

  // The open turn has no boundary yet.
  await page.evaluate(() => window.forkFixture.source("open"));
  await waitForDescribed(page, "This turn has not finished yet, so it has no boundary to branch from.");
  const open = await state(page, "open");
  assert.equal(open.disabled, "true", "an unfinished turn renders a disabled fork entry");
  assert.match(open.described, /has not finished yet/, "the unfinished turn names its reason");
  report.states.open = open;

  // A source that keeps no turn records proves no boundary.
  await page.evaluate(() => window.forkFixture.source("recordless"));
  await waitForDescribed(page, "This turn has no verifiable branch boundary in the session's records.");
  const recordless = await state(page, "recordless");
  assert.equal(recordless.disabled, "true", "a recordless source renders a disabled fork entry");
  assert.match(recordless.described, /no verifiable branch boundary/, "the recordless source names its reason");
  report.states.recordless = recordless;

  // Clicking the enabled entry creates the child through the real binding.
  await page.evaluate(() => window.forkFixture.source("completed"));
  await waitForLabel(page, "Branch into a new conversation");
  await entry(page).click();
  await entry(page).click();
  await page.waitForFunction(() => window.forkFixture.calls().length === 2, null, { timeout: 10_000 });
  await page.waitForFunction(() => document.querySelectorAll("[data-fork-call]").length === 2, null, { timeout: 10_000 });
  const calls = await page.evaluate(() => window.forkFixture.calls());
  const operations = await page.locator("[data-fork-call]").evaluateAll((nodes) => nodes.map((node) => node.textContent ?? ""));
  assert.equal(calls.length, 2, "each click dispatches one create");
  assert.deepEqual(calls.map((call) => call.turnId), ["turn-2", "turn-2"], "the click carries the turn identity");
  assert.ok(calls.every((call) => /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(call.operationId)),
    "each acknowledged host operation has an id");
  assert.notEqual(calls[0].operationId, calls[1].operationId, "the second acknowledged click is a new operation, not a replay");
  assert.deepEqual(operations, calls.map((call) => call.operationId), "the dispatched operation ids are the rendered ones");
  report.clicks = calls;

  // A created child whose tab did not open reaches the user as the recovery notice.
  await page.goto(`${base}?fork-attach-failure=1`);
  await page.locator(".chat-column .chat-turn-tail").last().waitFor();
  await waitForLabel(page, "Branch into a new conversation");
  await entry(page).click();
  await page.waitForFunction(() => (document.querySelector("[data-fork-notice]")?.textContent ?? "").includes("mock-fork-turn-"), null, { timeout: 10_000 });
  await entry(page).click();
  await page.waitForFunction(() => window.forkFixture.calls().length === 2, null, { timeout: 10_000 });
  const notice = await page.locator("[data-fork-notice]").textContent();
  const failed = await page.evaluate(() => window.forkFixture.calls());
  assert.equal(failed[1].operationId, failed[0].operationId, "an unacknowledged attach failure reuses the host operation");
  assert.ok(notice?.includes(failed[0].operationId), "the recovery notice names the child the failed attach produced");
  assert.match(notice ?? "", /could not open/, "the notice explains what happened to the child");
  assert.equal(await page.locator("[data-fork-notice]").count(), 1, "the failure is surfaced, never swallowed");
  report.notices.push(notice ?? "");

  // The same states and labels read in the second shipped locale. The zh-CN
  // dictionary arrives on demand, so each read waits for the localized text
  // the switch is expected to produce.
  await page.goto(base);
  await page.locator(".chat-column .chat-turn-tail").last().waitFor();
  await settle(page);
  const localeStartedAt = Date.now();
  await page.evaluate(() => window.forkFixture.locale("zh"));
  await waitForLabel(page, "在新对话中分支");
  // Recorded, not asserted: the provider loads a locale dictionary on demand and
  // renders its English fallback until that chunk lands.
  report.localeApplyMs = Date.now() - localeStartedAt;
  const zhCompleted = await state(page, "zh-completed");
  await page.evaluate(() => window.forkFixture.source("open"));
  await waitForDescribed(page, "该轮次尚未结束，还没有可供分支的边界。");
  const zhOpen = await state(page, "zh-open");
  await page.evaluate(() => window.forkFixture.source("recordless"));
  await waitForDescribed(page, "该轮次在会话记录中没有可确认的分支边界。");
  const zhRecordless = await state(page, "zh-recordless");
  assert.equal(zhCompleted.label, "在新对话中分支", "the enabled entry keeps the branch label in zh-CN");
  assert.equal(zhCompleted.disabled, null, "a completed turn stays forkable in zh-CN");
  assert.match(zhOpen.described, /该轮次尚未结束/, "the unfinished-turn reason is localized");
  assert.match(zhRecordless.described, /没有可确认的分支边界/, "the unverifiable reason is localized");
  report.states["zh-CN"] = { completed: zhCompleted, open: zhOpen, recordless: zhRecordless };

  await page.screenshot({ path: path.join(evidence, "fork-targets.png") });
  assert.deepEqual(errors, []);
  console.log(JSON.stringify(report, null, 2));
} finally {
  await browser.close();
  await server.httpServer.close();
  await rm(outDir, { recursive: true, force: true });
}
