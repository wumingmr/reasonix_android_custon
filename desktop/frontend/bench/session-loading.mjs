import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ??= path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const evidence = process.env.REASONIX_LOADING_EVIDENCE ?? "/tmp/reasonix-session-loading-evidence";
const server = await createServer({ root, logLevel: "error", server: { host: "127.0.0.1", port: 0 } });
await server.listen();
let browser;
try {
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, reducedMotion: "reduce" });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const address = server.httpServer.address();
  await page.goto(`http://127.0.0.1:${address.port}/bench/session-loading.html`);
  await page.waitForFunction(() => typeof window.paintLoading === "function");
  await page.clock.install();
  await page.clock.pauseAt(new Date(Date.now() + 1000));
  const paint = input => page.evaluate(input => window.paintLoading(input), input);
  const feedback = page.locator(".session-loading-indicator");
  const userNode = page.locator('.chat-node[data-chat-kind="user"]');
  await mkdir(evidence, { recursive: true });
  for (const surface of ["local", "remote", "transcript"]) {
    const input = { surface, identity: `${surface}-A`, phase: "loading" };
    await paint(input);
    await page.clock.runFor(249);
    assert.equal(await feedback.count(), 0, `${surface}: no loading UI for fast requests`);
    assert.equal(await page.locator(".session-recovery,.session-recovery-placeholder").count(), 0);
    await page.clock.runFor(1);
    assert.equal(await feedback.count(), 1, `${surface}: one delayed indicator`);
    const mainBefore = await page.locator(surface === "transcript" ? ".chat-transcript" : ".main").boundingBox();
    const shortText = await feedback.innerText();
    await page.clock.runFor(1750);
    assert.notEqual(await feedback.innerText(), shortText);
    assert.deepEqual(await page.locator(surface === "transcript" ? ".chat-transcript" : ".main").boundingBox(), mainBefore,
      "loading detail does not change the pane geometry");
    assert.equal(await feedback.evaluate(el => getComputedStyle(el).position), "absolute");
    assert.equal(await feedback.locator("svg").evaluate(el => getComputedStyle(el).animationName), "none", "respects reduced motion");
    await page.screenshot({ path: path.join(evidence, `${surface}-slow.png`) });

    await paint({ ...input, phase: "ready" });
    await page.clock.runFor(32);
    assert.equal(await feedback.count(), 0);
    assert.match(await userNode.innerText(), new RegExp(`Content ${input.identity}`));
    const nodeBefore = await userNode.boundingBox();
    await userNode.evaluate(el => { window.retainedLoadingNode = el; });
    await paint({ ...input, cached: true });
    await page.clock.runFor(2000);
    assert.equal(await userNode.evaluate(el => el === window.retainedLoadingNode), true);
    assert.deepEqual(await userNode.boundingBox(), nodeBefore, "cached content never shifts for loading feedback");
    assert.equal(await feedback.count(), 1);
    await page.screenshot({ path: path.join(evidence, `${surface}-cached.png`) });

    await paint({ ...input, identity: `${surface}-B` });
    assert.equal(await feedback.count(), 0, "switching hides the previous session's pending indicator");
    assert.equal(await page.locator(".chat-node").count(), 0, "the previous session never appears under a new identity");
    await page.clock.runFor(100);
    await paint({ ...input, identity: `${surface}-B`, phase: "ready" });
    await page.clock.runFor(2500);
    assert.equal(await feedback.count(), 0, "old deadlines cannot appear after completion");
    if (surface !== "transcript") {
      await paint({ ...input, phase: "error" });
      assert.equal(await page.locator('.session-recovery[role="alert"]').count(), 1);
      assert.equal(await feedback.count(), 0);
    }
    console.log(`PASS ${surface}: delayed feedback, immediate readiness, stable cached layout, session isolation and recovery`);
  }
  assert.deepEqual(errors, []);
  console.log(`Browser evidence: ${evidence}`);
} finally {
  await browser?.close();
  await server.close();
}
