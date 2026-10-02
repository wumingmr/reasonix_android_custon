import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { newSessionButton } from "./app-page-actions.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ??= path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const server = await createServer({ root, logLevel: "error", server: { host: "127.0.0.1", port: 0 } });
await server.listen();
let browser;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_EXECUTABLE });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const address = server.httpServer.address();
  await page.goto(`http://127.0.0.1:${address.port}/?mock=bench&bench=1`);
  const input = page.locator("textarea.composer__input:not([aria-hidden=true])");
  await input.waitFor();
  await page.evaluate(async () => {
    const { app } = await import("/src/lib/bridge.ts");
    const { DESKTOP_COMMANDS } = await import("/src/generated/desktopContract.generated.ts");
    const { installDesktopHostStub } = await import("/src/__tests__/desktopHostStub.ts");
    const fallback = Object.fromEntries(DESKTOP_COMMANDS.map(key => [key, app[key]]));
    const operations = new Map();
    const observedReady = new Set();
    const reads = new Map();
    const operationFor = meta => [...operations.values()].find(op => op.ref.sessionId === (meta.session?.sessionId || meta.sessionId));
    const startup = meta => {
      const operation = operationFor(meta);
      return operation?.phase === "starting" ? { ...meta, ready: false } : meta;
    };
    installDesktopHostStub({ ...fallback,
      BeginManualSessionCreation: async request => {
        // The mock creates the durable identity; hold runtime readiness behind
        // an explicit fixture completion, just like the host's reserved tab.
        const result = await fallback.BeginManualSessionCreation(request);
        const operation = { ...result, phase: "starting", surfaceReady: true };
        operations.set(request.operationId, operation);
        return structuredClone(operation);
      },
      GetManualSessionCreation: async id => {
        reads.set(id, (reads.get(id) || 0) + 1);
        const operation = operations.get(id);
        if (operation.phase === "ready") observedReady.add(id);
        return structuredClone(operation);
      },
      ListManualSessionCreations: async () => [...operations.values()].filter(op => op.phase !== "ready"),
      ListTabs: async () => (await fallback.ListTabs()).map(startup),
      MetaForTab: async id => startup(await fallback.MetaForTab(id)),
    });
    window.manualCreationFixture = {
      operations: () => [...operations.values()],
      finish: id => { operations.get(id).phase = "ready"; },
      observedReady: id => observedReady.has(id),
      reads: id => reads.get(id) || 0,
      active: async () => (await app.ListTabs()).find(tab => tab.active)?.session?.sessionId,
    };
  });
  await input.fill("Existing session input");
  await newSessionButton(page).click();
  await page.waitForFunction(() => window.manualCreationFixture.operations().length === 1);
  await input.waitFor({ state: "visible" });
  await page.waitForFunction(() => {
    const input = document.querySelector("textarea.composer__input:not([aria-hidden=true])");
    return input && !input.closest("[inert]") && !input.disabled && input.value === "";
  });
  const first = await page.evaluate(() => window.manualCreationFixture.operations()[0]);
  assert.equal(first.phase, "starting");
  await input.fill("First new session input while starting");
  assert.equal(await page.locator(".composer__btn--send").isDisabled(), true, "typing never sends before the new controller is ready");
  const notice = page.getByTestId("creation-notice");
  await notice.getByText(/You can type now/).waitFor();
  assert.equal(await notice.locator("button").count(), 0, "preparation does not ask for any user choice");
  const noticeBox = await notice.boundingBox();
  const inputBox = await input.boundingBox();
  assert.ok(noticeBox.y + noticeBox.height <= inputBox.y, "preparation explanation is adjacent to and above the composer");
  const evidence = process.env.REASONIX_CREATION_EVIDENCE ?? path.join(tmpdir(), "reasonix-manual-creation-evidence");
  await mkdir(evidence, { recursive: true });
  await page.screenshot({ path: path.join(evidence, "starting-editable.png") });

  await newSessionButton(page).click();
  await page.waitForFunction(() => window.manualCreationFixture.operations().length === 2);
  await page.waitForFunction(() => {
    const input = document.querySelector("textarea.composer__input:not([aria-hidden=true])");
    return input && !input.closest("[inert]") && input.value === "";
  });
  const second = await page.evaluate(() => window.manualCreationFixture.operations()[1]);
  assert.notEqual(second.ref.sessionId, first.ref.sessionId, "each explicit new action allocates its own session");
  await input.fill("Second new session input while starting");
  const firstReads = await page.evaluate(id => window.manualCreationFixture.reads(id), first.operationId);
  await page.evaluate(id => window.manualCreationFixture.finish(id), first.operationId);
  assert.equal(await page.evaluate(() => window.manualCreationFixture.active()), second.ref.sessionId);
  assert.equal(await input.inputValue(), "Second new session input while starting");
  await page.evaluate(id => window.manualCreationFixture.finish(id), second.operationId);
  await page.waitForFunction(id => window.manualCreationFixture.observedReady(id), second.operationId);
  await page.waitForFunction(() => !document.querySelector(".composer__btn--send")?.disabled);
  await notice.waitFor({ state: "detached" });
  assert.equal(await input.inputValue(), "Second new session input while starting", "runtime completion preserves typed input");
  assert.equal(await page.evaluate(() => window.manualCreationFixture.active()), second.ref.sessionId, "out-of-order startup never changes the user's selection");
  assert.equal(await page.evaluate(id => window.manualCreationFixture.reads(id), first.operationId), firstReads, "leaving a starting session stops its request polling while the host continues");
  assert.deepEqual(errors, []);
  await page.screenshot({ path: path.join(evidence, "ready-input-preserved.png") });
  console.log(`PASS manual creation: editable starting input, isolated new identities, send readiness and out-of-order completion. Evidence: ${evidence}`);
} finally {
  await browser?.close();
  await server.close();
}
