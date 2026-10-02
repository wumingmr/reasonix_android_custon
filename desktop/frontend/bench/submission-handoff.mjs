import assert from "node:assert/strict";
import { mkdtemp, copyFile, writeFile, mkdir, rm } from "node:fs/promises";
import { writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { verifyNativeReaderInput } from "./submission-native-input.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = path.join(root, ".pw-browsers");
const { chromium, _electron } = await import("playwright");
const electron = process.argv.includes("--electron");
const nativeInput = process.argv.includes("--native-input");
assert.ok(!nativeInput || electron, "native input requires Electron");
const evidence = process.env.REASONIX_HANDOFF_EVIDENCE ?? path.join(tmpdir(), "reasonix-handoff-evidence");
await mkdir(evidence, { recursive: true });
const server = await createServer({ root, server: { host: "127.0.0.1", port: 0 } });
await server.listen();
const url = `http://127.0.0.1:${server.httpServer.address().port}/bench/submission-handoff.html`;
console.log(url);
const host = await mkdtemp(path.join(tmpdir(), "reasonix-handoff-host-"));
let browser, app;
const report = { host: electron ? "electron" : "chromium", actions: [], rounds: [], errors: [], nativeInput: "not_run", complete: false };
try {
  let page;
  if (electron) {
    await copyFile(path.join(root, nativeInput ? "bench/submission-native-electron.cjs" : "bench/transcript-layout-electron.cjs"), path.join(host, "main.cjs"));
    app = await _electron.launch({ executablePath: createRequire(path.join(root, "../electron/package.json"))("electron"), args: [path.join(host, "main.cjs")], env: { ...process.env, REASONIX_LAYOUT_URL: url } });
    page = await app.firstWindow();
  } else {
    browser = await chromium.launch({ headless: true });
    page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  }
  page.on("pageerror", error => report.errors.push(error.message));
  page.on("console", message => { if (message.type() === "error" && /same key|unique.*key|Maximum update|Uncaught/i.test(message.text())) report.errors.push(message.text()); });
  await page.addInitScript(() => { window.handoffWrites = []; window.__REASONIX_TRANSCRIPT_SCROLL_WRITE__ = write => window.handoffWrites.push(write); });
  await page.goto(url);
  await page.waitForFunction(() => Boolean(window.handoff));
  const frame = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.locator('[data-chat-kind="user"]').first().waitFor();
  await frame();
  const input = page.locator("textarea.composer__input:not(.composer__input--measure)");
  await input.fill("handoff question"); await input.press("Enter");
  report.actions.push("composer-submit", "live-output", "rpc-accepted", "output-completed", "expand-process", "batched-identity-and-message-only-record");
  await page.waitForFunction(() => window.handoff.inspect().locals === 1);
  await page.evaluate(() => window.handoff.output()); await frame();
  await page.evaluate(() => window.handoff.finish()); await frame();
  const disclosure = page.locator('[data-chat-kind="process"]').last().locator('button[aria-expanded]');
  await disclosure.click();
  await frame();
  assert.equal(await disclosure.getAttribute("aria-expanded"), "true");
  await page.evaluate(() => {
    window.beforeHandoff = Object.fromEntries(["user", "process", "tail"].map(kind => [kind, [...document.querySelectorAll(`[data-chat-kind="${kind}"]`)].at(-1)]));
    const text = window.beforeHandoff.user.querySelector(".msg__body");
    if (text?.firstChild) { const range = document.createRange(); range.selectNodeContents(text); getSelection().removeAllRanges(); getSelection().addRange(range); }
    window.selectionBefore = getSelection().toString();
    window.focusBefore = document.activeElement;
    window.writeCountBefore = window.handoffWrites.length;
    window.handoff.batched();
  });
  await page.waitForFunction(() => window.handoff.inspect().locals === 0); await frame();
  report.handoff = await page.evaluate(() => ({
    sameNodes: Object.fromEntries(Object.entries(window.beforeHandoff).map(([kind, node]) => [kind, Boolean(node && node === [...document.querySelectorAll(`[data-chat-kind="${kind}"]`)].at(-1))])),
    selectionPreserved: getSelection().toString() === window.selectionBefore,
    focusPreserved: document.activeElement === window.focusBefore,
    processExpanded: [...document.querySelectorAll('[data-chat-kind="process"]')].at(-1)?.querySelector('button')?.getAttribute("aria-expanded"),
    newWrites: window.handoffWrites.slice(window.writeCountBefore), ...window.handoff.inspect(),
  }));
  assert.ok(Object.values(report.handoff.sameNodes).every(Boolean), JSON.stringify(report.handoff.sameNodes));
  assert.ok(report.handoff.selectionPreserved);
  assert.ok(report.handoff.focusPreserved);
  assert.equal(report.handoff.processExpanded, "true");
  assert.equal(report.handoff.ids.filter(id => id === "m:sent-0").length, 1);
  await page.evaluate(() => window.handoff.finish()); await frame();

  for (let round = 0; round < 20; round++) {
    report.actions.push({ round, direction: "older", pages: 4 });
    await page.evaluate(async () => { for (let step = 0; step < 4; step++) await window.handoff.page("older"); }); await frame();
    let sample = await page.evaluate(() => window.handoff.inspect());
    assert.ok(sample.stats.residentWindowEntries <= 96);
    assert.ok(sample.handoffs <= sample.users);
    assert.ok(sample.presentation.messages <= sample.users);
    assert.equal(sample.locals, 0);
    assert.equal(sample.ids.includes("m:sent-0"), false);
    if (round === 0) {
      report.actions.push("reader-wheel", "offscreen-submit", "offscreen-record");
      if (nativeInput) {
        report.nativeInput = {};
        const saveProgress = () => writeFileSync(path.join(evidence, "native-input-progress.json"), JSON.stringify(report.nativeInput, null, 2));
        const progress = setInterval(saveProgress, 2000);
        const deadline = setTimeout(() => {
          report.nativeInput.timeout = true;
          saveProgress();
          app.process().kill("SIGKILL");
        }, 90_000);
        try { await verifyNativeReaderInput(page, report.nativeInput); }
        catch (error) {
          await page.screenshot({ path: path.join(evidence, "native-input-failure.png"), timeout: 5000 }).catch(() => {});
          throw error;
        } finally {
          clearInterval(progress); clearTimeout(deadline); saveProgress();
        }
      }
      else {
        await page.locator(".chat-flow-scroll").hover();
        await page.mouse.wheel(0, -300);
      }
      await page.waitForFunction(() => document.querySelector(".chat-flow-scroll")?.getAttribute("data-scroll-mode") === "reader");
      await frame();
      await page.evaluate(() => { window.handoff.send("offscreen question"); window.readerIds = window.handoff.inspect().ids; }); await frame();
      await page.evaluate(() => {
        const row = document.querySelector('[data-chat-kind="user"]');
        window.readerNode = row; window.readerTop = row.getBoundingClientRect().top;
        window.writeCountBefore = window.handoffWrites.length;
        window.handoff.record();
      });
      await page.waitForFunction(() => window.handoff.inspect().locals === 0); await frame();
      report.offscreen = await page.evaluate(() => ({ sameIds: JSON.stringify(window.readerIds) === JSON.stringify(window.handoff.inspect().ids),
        sameNode: window.readerNode.isConnected, drift: Math.abs(window.readerNode.getBoundingClientRect().top - window.readerTop),
        newWrites: window.handoffWrites.slice(window.writeCountBefore) }));
      assert.ok(report.offscreen.sameIds && report.offscreen.sameNode);
      assert.ok(report.offscreen.drift <= 1);
      assert.ok(report.offscreen.newWrites.every(write => write.owner === "restore" && write.outcome === "no-op"), "confirmation must not scroll the reader or request tail-follow");
      await page.evaluate(() => window.handoff.finish());
    }
    report.actions.push({ round, direction: "newer", pages: 4 });
    await page.evaluate(async () => { for (let step = 0; step < 4; step++) await window.handoff.page("newer"); }); await frame();
    sample = await page.evaluate(() => window.handoff.inspect());
    assert.ok(sample.stats.residentWindowEntries <= 96);
    assert.ok(sample.handoffs <= sample.users);
    assert.ok(sample.presentation.messages <= sample.users);
    assert.ok(sample.presentation.submissions <= sample.users + sample.locals);
    assert.equal(new Set(sample.ids).size, sample.ids.length);
    assert.equal(sample.ids.filter(id => id === "m:sent-0").length, 1, "reclaimed message must reload exactly once");
    assert.equal(await page.locator('[data-chat-kind="user"]').filter({ hasText: "handoff question" }).count(), 1);
    assert.equal(await page.locator('[data-chat-kind="user"]').filter({ hasText: "offscreen question" }).count(), 1);
    assert.equal(sample.locals, 0);
    report.rounds.push(sample);
  }
  assert.ok(report.rounds.at(-1).stats.reclaimedPages > 0);
  assert.deepEqual(report.errors, []);
  report.complete = true;
  console.log(JSON.stringify({ host: report.host, complete: true, rounds: report.rounds.length, handoff: report.handoff.sameNodes, offscreen: report.offscreen, stats: report.rounds.at(-1).stats }));
} finally {
  await writeFile(path.join(evidence, `${report.host}.json`), JSON.stringify(report, null, 2));
  await app?.close(); await browser?.close(); await server.close(); await rm(host, { recursive: true, force: true });
}
