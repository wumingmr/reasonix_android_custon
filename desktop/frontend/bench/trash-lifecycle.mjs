// Run against a local Vite server. The real React surface uses a deterministic
// host fixture; Go tests separately exercise the same RPC admission invariants.
import assert from "node:assert/strict";
import { chromium } from "playwright";
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  const openDelete = async () => {
    await page.locator(".archived-sessions__open").click();
    await page.getByRole("button", { name: "Conversation actions" }).click();
    await page.getByRole("menuitem", { name: "Permanently delete" }).click();
  };
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const url = process.env.REASONIX_TRASH_BROWSER_URL || "http://127.0.0.1:4771/bench/trash-lifecycle.html";
  await page.goto(url);
  await openDelete();
  await page.getByRole("dialog").waitFor();
  await page.evaluate(() => window.trashFixture.changeTarget());
  await page.getByRole("button", { name: "Permanently delete", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "changed state" }).waitFor();
  assert.equal(await page.evaluate(() => window.trashFixture.requests[0].expectedGeneration), 7);
  assert.equal(await page.getByRole("button", { name: "Retry failed items" }).count(), 0);

  await page.reload();
  await page.locator(".archived-sessions__open").click();
  await page.evaluate(() => window.trashFixture.invalidatePreview());
  await page.locator(".archived-sessions__preview").waitFor({ state: "detached" });
  await page.evaluate(() => window.trashFixture.finishPreview());
  assert.equal(await page.getByText("late fixture history").count(), 0);

  await page.reload();
  await openDelete();
  await page.evaluate(() => { window.trashFixture.changeOther(); window.trashFixture.loseResult(); });
  await page.getByRole("button", { name: "Permanently delete", exact: true }).click();
  await page.getByRole("button", { name: "Retry failed items" }).waitFor();
  await page.evaluate(() => window.trashFixture.changeOther());
  await page.getByRole("button", { name: "Retry failed items" }).click();
  await page.waitForFunction(() => window.trashFixture.requests.length === 2);
  const requests = await page.evaluate(() => window.trashFixture.requests);
  assert.deepEqual(requests[0], requests[1]);
  assert.equal(requests[1].expectedGeneration, 7);
  await page.waitForFunction(() => document.querySelector('[role="status"]')?.textContent?.includes("1"));
  assert.deepEqual(errors, []);
  console.log("PASS Chromium confirmation snapshot, conflict guidance, preview invalidation, unchanged retry after refresh");
} finally { await browser.close(); }
