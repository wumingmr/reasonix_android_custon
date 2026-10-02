import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
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
  const page = await browser.newPage({ viewport: { width: 1100, height: 600 }, locale: "en-US" });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/bench/manual-creation-recovery.html`);
  await page.getByText(/You can type now/).waitFor();
  assert.doesNotMatch(await page.locator("body").innerText(), /\/private\/old-project|global/);
  const retry = page.getByRole("button", { name: "Try again", exact: true });
  assert.equal(await retry.count(), 0, "automatic lock waiting needs no user decision");
  await page.evaluate(() => window.creationFixture.setStatus("waiting_workspace"));
  await page.getByText(/project is temporarily unavailable/).waitFor();
  assert.equal(await retry.count(), 0, "temporary directory recovery needs no user decision");
  await page.evaluate(() => window.creationFixture.setStatus("blocked"));
  await page.getByText(/could not be prepared/).waitFor();
  await page.getByText("Details", { exact: true }).click();
  await page.getByRole("button", { name: "Export creation diagnostics" }).click();
  assert.equal((await page.evaluate(() => window.creationFixture.counts())).exports, 1);
  const evidence = process.env.REASONIX_CREATION_EVIDENCE ?? path.join(tmpdir(), "reasonix-creation-evidence");
  await mkdir(evidence, { recursive: true });
  await page.screenshot({ path: path.join(evidence, "recovery.png") });
  await retry.click();
  assert.equal(await retry.isDisabled(), true);
  assert.equal((await page.evaluate(() => window.creationFixture.counts())).retries, 1);
  await page.evaluate(() => window.creationFixture.finish());
  await page.getByText(/You can type now/).waitFor();
  assert.equal(await retry.count(), 0);
  assert.equal(await page.locator("button").count(), 0);
  assert.deepEqual(errors, []);
  console.log("PASS Chromium: automatic lock/directory recovery, coalesced failure retry, initialization feedback and optional diagnostics; no page errors");
} finally {
  await browser?.close();
  await server.close();
}
