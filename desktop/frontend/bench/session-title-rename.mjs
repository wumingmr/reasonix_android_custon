// Real sidebar interaction with a controlled host. Durable writes are covered
// by Desktop tests; this gate checks per-target UI state and transcript identity.
import path from "node:path";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
import { createServer } from "vite";
import { chromium } from "playwright";
import { selectSession } from "./app-page-actions.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const server = await createServer({ root, logLevel: "error", server: { host: "127.0.0.1", port: 4679, strictPort: true } });
await server.listen();
let browser;
try {
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.setDefaultTimeout(20000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto("http://127.0.0.1:4679/?mock=bench&bench=1");
  const composer = page.locator("textarea.composer__input:not([aria-hidden=true])");
  await composer.waitFor();
  await selectSession(page, "bench:small-6t");
  await page.waitForFunction(() => document.querySelector(".transcript")?.textContent?.includes("ASYNC LAYOUT EXPANSION COMPLETE"));
  await composer.fill("keep my draft");
  await page.evaluate(async () => {
    const { app } = await import("/src/lib/bridge.ts");
    const { installDesktopHostStub } = await import("/src/__tests__/desktopHostStub.ts");
    const { DESKTOP_COMMANDS } = await import("/src/generated/desktopContract.generated.ts");
    const fallback = Object.fromEntries(DESKTOP_COMMANDS.map(key => [key, app[key]]));
    window.__titleFixture = { requests: [], composer: document.querySelector("textarea.composer__input:not([aria-hidden=true])"), nodes: [...document.querySelectorAll("[data-chat-anchor-key]")] };
    installDesktopHostStub({ ...fallback, AIRenameSessionTarget: selector => new Promise((resolve, reject) => {
      window.__titleFixture.requests.push({
        target: selector.ref?.sessionId ? `session-id:${selector.ref.sessionId}` : selector.sessionPath || selector.topicId,
        resolve: title => resolve({ targetKey: selector.sessionPath || selector.topicId || selector.ref?.sessionId || "", operationId: crypto.randomUUID(), committed: true, title, lifecycleGeneration: 1 }),
        reject,
      });
    }) });
  });
  const rows = page.locator(".project-tree__topic:not(.project-tree__topic--active)");
  const names = await rows.locator(".project-tree__topic-label").allTextContents();
  assert(names.length >= 2, "fixture needs two background sessions");
  const row = name => page.locator(".project-tree__topic").filter({ has: page.locator(".project-tree__topic-label", { hasText: name }) }).first();
  const aiItem = () => page.getByRole("menuitem", { name: /AI.*(rename|Rename)|AI.*命名/ });
  await row(names[0]).click({ button: "right" });
  await aiItem().click();
  await page.waitForFunction(() => window.__titleFixture.requests.length === 1);
  await row(names[0]).click({ button: "right" });
  assert(await page.getByRole("menuitem", { name: /renaming|命名中/i }).isDisabled(), "target alone has loading state");
  await page.keyboard.press("Escape");
  await row(names[1]).click({ button: "right" });
  assert(await aiItem().isEnabled(), "another session remains independently available");
  await aiItem().click();
  await page.waitForFunction(() => window.__titleFixture.requests.length === 2);
  assert(await page.evaluate(() => window.__titleFixture.requests[0].target !== window.__titleFixture.requests[1].target), "requests retain different identities");
  await page.evaluate(() => window.__titleFixture.requests[1].resolve("Second completed"));
  await page.getByText(/Second completed/, { exact: false }).last().waitFor();
  await page.evaluate(() => window.__titleFixture.requests[0].reject(new Error("controller lease /private/fixture test-secret")));
  await page.getByText(/Unable to complete this session operation|无法完成该会话操作|無法完成此會話操作/).waitFor();
  assert(!await page.locator("body").textContent().then(text => text.includes("test-secret")), "raw host errors never reach the toast");
  assert.equal(await composer.inputValue(), "keep my draft");
  const unchanged = await page.evaluate(() => {
    const f = window.__titleFixture;
    const nodes = [...document.querySelectorAll("[data-chat-anchor-key]")];
    return f.composer === document.querySelector("textarea.composer__input:not([aria-hidden=true])") && nodes.length === f.nodes.length && nodes.every((node, i) => node === f.nodes[i]);
  });
  assert(unchanged, "background rename preserves transcript hosts and composer");
  assert.deepEqual(errors, []);
  console.log("PASS sidebar per-target loading, out-of-order completion, sanitized errors, stable transcript and draft");
} finally {
  await browser?.close();
  await server.close();
}
