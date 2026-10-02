import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { newSessionButton } from "./app-page-actions.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const { chromium } = await import("playwright");
const server = await createServer({ root, logLevel: "error", server: { host: "127.0.0.1", port: 0 } });
await server.listen();
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/?mock=bench&bench=1`);
  const composer = page.locator("textarea.composer__input:not([aria-hidden=true])");
  await composer.waitFor();
  const original = await page.evaluate(async () => (await (await import("/src/lib/bridge.ts")).app.GetWorkspaceSnapshot()).workspaces.flatMap(workspace => workspace.sessionIds));
  // Exercise actual clicks; every accepted click must survive navigation coalescing.
  for (let index = 0; index < 3; index++) await newSessionButton(page).click();
  await page.waitForFunction(async before => {
    const { app } = await import("/src/lib/bridge.ts");
    return (await app.GetWorkspaceSnapshot()).workspaces.flatMap(workspace => workspace.sessionIds).filter(id => !before.includes(id)).length === 3;
  }, original);
  const created = await page.evaluate(async before => (await (await import("/src/lib/bridge.ts")).app.GetWorkspaceSnapshot()).workspaces.flatMap(workspace => workspace.sessionIds).filter(id => !before.includes(id)), original);
  assert.equal(new Set(created).size, 3);
  await page.waitForFunction(() => document.querySelector("textarea.composer__input:not([aria-hidden=true])")?.disabled === false);
  await composer.fill("formal session unsent input");
  await page.waitForFunction(async () => {
    const { app } = await import("/src/lib/bridge.ts");
    const active = (await app.ListTabs()).find(tab => tab.active);
    return active && (await app.GetSessionComposerState({hostId:"local",sessionId:active.sessionId})).contentJson.includes("formal session unsent input");
  });
  assert.deepEqual(await page.evaluate(async () => (await import("/src/lib/bridge.ts")).app.ListSessionDraftSummaries()), []);
  assert.deepEqual(errors, []);
  console.log("PASS real UI: three New actions create distinct formal sessions; input persists; no legacy draft is created");
} finally { await browser.close(); await server.close(); }
