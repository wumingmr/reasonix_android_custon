import assert from "node:assert/strict";
import path from "node:path";
import fs from "node:fs/promises";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ??= path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const port = Number(process.env.REASONIX_INDEPENDENT_BROWSER_PORT ?? 4687);
const server = await createServer({ root, logLevel: "error", server: { host: "127.0.0.1", port, strictPort: true, hmr: false, watch: { ignored: ["**"] } } });
await server.listen();
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, locale: "en-US" });
const errors = [];
page.on("pageerror", error => errors.push(error.message));
const evidence = process.env.REASONIX_INDEPENDENT_EVIDENCE ?? path.join(tmpdir(), "reasonix-independent-browser");
const row = title => page.locator(".project-tree__topic-main").filter({ hasText: title });
async function dragRow(sourceTitle, target, position = "before") {
  const source = row(sourceTitle).locator("..");
  const transfer = await page.evaluateHandle(() => new DataTransfer());
  await source.dispatchEvent("dragstart", { dataTransfer: transfer });
  const box = await target.boundingBox();
  const point = { clientX: box.x + box.width / 2, clientY: position === "before" ? box.y + 2 : box.y + box.height - 2, dataTransfer: transfer };
  await target.dispatchEvent("dragover", point);
  await target.dispatchEvent("drop", point);
  await source.dispatchEvent("dragend", { dataTransfer: transfer });
  await transfer.dispose();
}
try {
  await fs.mkdir(evidence, { recursive: true });
  await page.goto(`http://127.0.0.1:${port}/bench/independent-sessions.html`);
  await row("Session A").waitFor();
  await row("Session B").waitFor();
  assert.equal(await page.locator(".project-tree__topic-main").count(), 2);
  const x = await page.locator(".project-tree__topic-main").evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().x));
  assert.equal(x[0], x[1], "ordinary forks occupy sibling rows");
  await page.waitForFunction(() => JSON.parse(localStorage.getItem("projectTree:readActivity:v3") || "{}").records?.["ref\x00local\x00b"]?.value === 10);
  await page.getByRole("button", { name: "Advance B activity observation", exact: true }).click();
  await page.locator(".project-tree__topic--unread").filter({ hasText: "Session B" }).waitFor();
  assert.equal(await page.locator(".project-tree__topic--unread").count(), 1, "B result 20 is unread independently of A result 100");
  await row("Session A").focus();
  await row("Session A").press("Enter");
  assert.equal(await page.locator(".project-tree__topic--unread").filter({ hasText: "Session B" }).count(), 1, "reading A leaves B unread");
  await row("Session B").focus();
  await row("Session B").press("Enter");
  await page.waitForFunction(() => document.querySelector(".topicbar h1")?.textContent === "Session B");
  assert.equal(await page.locator(".project-tree__topic--active").count(), 1, "keyboard opening selects B alone");
  await page.waitForFunction(() => JSON.parse(localStorage.getItem("projectTree:readActivity:v3") || "{}").records?.["ref\x00local\x00b"]?.value === 20);
  assert.equal(await page.locator(".project-tree__topic--unread").count(), 0, "opening B clears only B unread");
  await row("Session A").click();
  await page.waitForFunction(() => document.querySelector(".topicbar h1")?.textContent === "Session A");
  for (const sequence of [10, 20]) {
    await page.getByRole("button", { name: "Advance B activity observation", exact: true }).click();
    await page.getByRole("button", { name: "Remount sidebar", exact: true }).click();
    await row("Session B").waitFor();
    assert.equal(await page.evaluate(() => JSON.parse(localStorage.getItem("projectTree:readActivity:v3")).records["ref\x00local\x00b"].value), 20, `ready result ${sequence} cannot lower B baseline`);
    assert.equal(await page.locator(".project-tree__topic--unread").count(), 0, `ready result ${sequence} cannot create false unread`);
  }
  await row("Session B").click();
  await page.waitForFunction(() => document.querySelector(".topicbar h1")?.textContent === "Session B");
  await dragRow("Session B", row("Session A").locator(".."));
  await page.waitForFunction(() => window.__independentEvidence.organizationCalls.some(call => call.kind === "move"));
  await page.waitForFunction(() => document.querySelector(".project-tree__topic-main")?.textContent?.includes("Session B"));
  assert.deepEqual(await page.locator(".project-tree__topic-main").evaluateAll(nodes => nodes.map(node => node.textContent?.match(/Session [AB]/)?.[0])), ["Session B", "Session A"]);
  // Group creation belongs to the expanded creation sidebar; compact workbench
  // deliberately exposes a smaller project menu.
  await page.getByRole("button", { name: "Toggle creation", exact: true }).click();
  await page.locator(".project-tree__folder-main").filter({ hasText: "Independent sessions" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "New group", exact: true }).click();
  const group = page.locator(".project-tree__group-main").filter({ hasText: "New group" });
  await group.waitFor();
  await page.keyboard.press("Escape");
  await dragRow("Session B", group);
  await page.waitForFunction(() => window.__independentEvidence.organizationCalls.some(call => call.kind === "set-group" && call.groupId));
  assert.equal(await page.locator(".project-tree__group").filter({ hasText: "New group" }).locator(".project-tree__topic-main").count(), 1);
  assert.equal(await page.locator(".project-tree__group").filter({ hasText: "New group" }).getByText("Session B", { exact: true }).count(), 1);
  await row("Session B").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Remove from group", exact: true }).click();
  await page.waitForFunction(() => window.__independentEvidence.organizationCalls.some(call => call.kind === "set-group" && call.groupId === ""));
  await page.getByRole("button", { name: "Remount sidebar", exact: true }).click();
  await row("Session B").waitFor();
  assert.equal(await page.locator(".project-tree__group").locator(".project-tree__topic-main").count(), 0, "explicitly ungrouped B remains ungrouped after remount");
  await page.getByRole("button", { name: "Toggle creation", exact: true }).click();
  await page.getByRole("button", { name: "Show selected runtime approval", exact: true }).click();
  await page.getByRole("option", { name: /Allow once/i }).click();
  await page.getByRole("button", { name: "Confirm", exact: true }).click();
  await page.waitForFunction(() => window.__independentEvidence.runtimeCalls.some(call => call.method === "approve"));
  await page.getByRole("button", { name: "Show selected runtime approval", exact: true }).click();
  await page.getByRole("button", { name: "Stop task", exact: true }).click();
  await page.waitForFunction(() => window.__independentEvidence.runtimeCalls.some(call => call.method === "stop"));
  assert.deepEqual(await page.evaluate(() => window.__independentEvidence.runtimeCalls), [
    { method: "approve", tabId: "tab-b", promptId: "approval-b", turnId: "turn-tab-b", epoch: "epoch-tab-b" },
    { method: "stop", tabId: "tab-b" },
  ]);
  await row("Session B").click();
  await page.waitForFunction(() => document.querySelector(".topicbar h1")?.textContent === "Session B");
  assert.equal(await page.locator(".project-tree__topic--active").count(), 1);
  await page.getByRole("button", { name: "Rename selected session", exact: true }).click();
  await page.locator(".topicbar__title-input").fill("Only B from top");
  await page.locator(".topicbar__title-input").press("Enter");
  await row("Only B from top").waitFor();
  assert.equal(await row("Session A").count(), 1);
  await row("Only B from top").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Rename session", exact: true }).click();
  await page.locator(".project-tree__topic-input").waitFor();
  await page.locator(".project-tree__topic-input").fill("Only B from sidebar");
  await page.locator(".project-tree__topic-input").press("Enter");
  await row("Only B from sidebar").waitFor();
  assert.equal(await row("Session A").count(), 1, "sidebar rename must preserve same-topic A");
  await page.getByRole("button", { name: "Open fixture history", exact: true }).click();
  const historyB = page.locator(".hist-item").filter({ hasText: "Only B from sidebar" });
  await historyB.locator(".hist-item__main").click();
  await page.locator(".history-preview").getByRole("button", { name: "Rename", exact: true }).click();
  await page.locator(".hist-item__rename").fill("Only B from history");
  await page.locator(".hist-item__rename").press("Enter");
  await page.waitForFunction(() => window.__independentEvidence.calls.some(call => call.title === "Only B from history"));
  await page.locator(".history-modal").getByRole("button", { name: "Close", exact: true }).click();
  await page.locator(".history-modal").waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "Remount sidebar", exact: true }).click();
  await row("Only B from history").waitFor();
  for (const theme of ["Light", "Dark"]) {
    await page.getByRole("button", { name: `${theme} theme`, exact: true }).click();
    assert.equal(await page.locator("html").getAttribute("data-theme"), theme.toLowerCase());
    await page.screenshot({ path: path.join(evidence, `workbench-${theme.toLowerCase()}.png`) });
  }
  await page.getByRole("button", { name: "Toggle creation", exact: true }).click();
  await page.locator(".app--creation").waitFor();
  assert.equal(await page.locator(".project-tree__topic-main").count(), 2);
  await row("Session A").click();
  await page.waitForFunction(() => document.querySelector(".topicbar h1")?.textContent === "Session A");
  await page.screenshot({ path: path.join(evidence, "creation-dark.png") });
  await page.getByRole("button", { name: "Light theme", exact: true }).click();
  await page.screenshot({ path: path.join(evidence, "creation-light.png") });
  await page.getByRole("button", { name: "Toggle creation", exact: true }).click();
  await row("Only B from history").click({ button: "right" });
  await page.getByRole("menuitem", { name: /Move to trash/i }).click();
  await page.getByRole("menuitem", { name: /Confirm/i }).click();
  await row("Only B from history").waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "Inject late snapshots", exact: true }).click();
  await page.getByRole("button", { name: "Remount sidebar", exact: true }).click();
  await row("Session A").waitFor();
  await page.waitForTimeout(300);
  assert.equal(await page.locator(".project-tree__topic-main").count(), 1, "archived B remains fenced across stale snapshots and remount");
  const calls = await page.evaluate(() => window.__independentEvidence.calls);
  assert.deepEqual(calls, [
    { method: "rename", id: "b", title: "Only B from top" },
    { method: "rename", id: "b", title: "Only B from sidebar" },
    { method: "rename", id: "b", title: "Only B from history" },
    { method: "archive", id: "b" },
  ], "each user action writes exactly once to B and never to same-topic A");
  assert.deepEqual(errors, []);
  const organizationCalls = await page.evaluate(() => window.__independentEvidence.organizationCalls);
  assert.equal(organizationCalls.length, 4, "one move, one group creation and two exact B membership changes");
  assert.ok(organizationCalls.filter(call => call.target).every(call => call.target.ref.sessionId === "b"));
  const runtimeCalls = await page.evaluate(() => window.__independentEvidence.runtimeCalls);
  // A fresh mounted real tree initially shows five old-snapshot rows. Its old
  // continuation fails, so the UI must discard that window and read page one.
  await page.goto(`http://127.0.0.1:${port}/bench/independent-sessions.html?pagination=1`);
  await row("Session A").waitFor();
  assert.equal(await page.locator(".project-tree__topic-main").count(), 5);
  await page.getByRole("button", { name: /Show more/i }).click();
  await row("Session C").waitFor();
  await row("Session G").waitFor();
  assert.deepEqual(await page.locator(".project-tree__topic-label").allTextContents(), ["Session C", "Session A", "Session B", "Session D", "Session E", "Session F", "Session G"], "stale cursor recovery replaces the old window without omissions or duplicate rows");
  const pageRequests = await page.evaluate(() => window.__independentEvidence.pageRequests);
  const rejection = pageRequests.findIndex(request => request.rejected);
  assert.ok(rejection >= 0);
  assert.equal(pageRequests[rejection].cursor, "old:5");
  assert.equal(pageRequests[rejection + 1].cursor, "", "stale cursor recovery must restart at page one");
  await page.screenshot({ path: path.join(evidence, "pagination-recovered.png") });
  for (const size of [205, 405, 1000]) {
    await page.goto(`http://127.0.0.1:${port}/bench/independent-sessions.html?window=${size}`);
    await page.waitForFunction((size) => document.querySelectorAll(".project-tree__topic-main").length === size, size);
    const labels = await page.locator(".project-tree__topic-label").allTextContents();
    assert.equal(new Set(labels).size, size, "recovered whole window has no duplicates");
    const requests = await page.evaluate(() => window.__independentEvidence.pageRequests);
    assert.ok(requests.some((request) => request.cursor === "old:200" && request.rejected));
    assert.equal(requests.filter((request) => request.cursor === "").length, 2, "exactly one automatic recovery");
  }
  assert.deepEqual(errors, []);
  await fs.writeFile(path.join(evidence, "result.json"), JSON.stringify({ passed: true, calls, organizationCalls, runtimeCalls, pageRequests, unreadChecks: ["B 10→20 unread while A=100", "reading A preserves B unread", "reading B clears its unread", "stale ready 10 then20 preserves baseline20 across remount"], errors, scope: "Chromium product components + actual command owners, controlled Desktop RPC; approval/stop use real prompt card (sidebar has no such action)" }, null, 2));
  console.log("PASS independent-session browser: sibling rows, keyboard open, exact group/drag order, approval/stop target, top/history rename, light/dark, creation, archive stale runtime/remount");
} catch (error) {
  console.error("Browser errors:", errors);
  console.error("Boundary evidence:", await page.evaluate(() => window.__independentEvidence));
  console.error("Visible text:", (await page.locator("body").innerText()).slice(0, 4000));
  throw error;
} finally { await browser.close(); await server.close(); }
