import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ??= path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const server = await createServer({ root, logLevel: "error", server: { host: "127.0.0.1", port: 0 } });
await server.listen();
let browser;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_EXECUTABLE });
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/bench/draft-recovery.html`);
  const alert = page.locator(".session-draft-attention");
  await alert.waitFor();
  assert.equal(await alert.locator(".session-draft-surface__error").count(), 1);
  assert.equal(await alert.locator("button").count(), 1, "only submission recovery is offered");
  assert.match(await alert.locator('[role="status"]').innerText(), /Session startup|会话启动|工作階段啟動/);
  await alert.locator("button").click();
  assert.equal(await page.evaluate(() => window.draftRecoveryRetries()), 1);
  const evidence = process.env.REASONIX_DRAFT_EVIDENCE ?? "/tmp/reasonix-draft-recovery-evidence";
  await mkdir(evidence, { recursive: true });
  await page.screenshot({ path: path.join(evidence, "runtime-failure.png") });
  await page.evaluate(() => window.paintDraftRecovery(true));
  assert.equal(await alert.locator("button").isDisabled(), true);
  assert.deepEqual(errors, []);
  console.log("PASS real Chromium: distinct startup error, one error row, resume routing, retry disabled while pending");
} finally {
  await browser?.close();
  await server.close();
}
