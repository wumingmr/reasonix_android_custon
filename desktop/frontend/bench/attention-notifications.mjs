#!/usr/bin/env node
// Real app UI and Web Audio, with controlled background runtime snapshots.
import assert from "node:assert/strict";
import path from "node:path";
import fs from "node:fs/promises";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { selectSession, readActiveSessionLabel } from "./app-page-actions.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
if (process.env.PLAYWRIGHT_BROWSERS_PATH === ".pw-browsers" ||
    (!process.env.PLAYWRIGHT_BROWSERS_PATH && existsSync(path.join(root, ".pw-browsers")))) {
  process.env.PLAYWRIGHT_BROWSERS_PATH = path.join(root, ".pw-browsers");
}
const { chromium } = await import("playwright");
const server = await createServer({ root, logLevel: "error", server: { host: "127.0.0.1", port: 0 } });
await server.listen();
const url = server.resolvedUrls.local[0];
console.log("Attention browser fixture: " + url);
let browser;
let releaseNotificationModule;
try {
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [];
  const notificationModuleGate = new Promise(resolve => { releaseNotificationModule = resolve; });
  await page.route("**/src/lib/runtimeNotifications.ts", async route => {
    await notificationModuleGate;
    await route.continue();
  });
  page.on("pageerror", error => errors.push(error.message));
  page.on("console", message => { if (message.type() === "error") errors.push(message.text()); });
  await page.goto(url + "?mock=bench&bench=1", { waitUntil: "domcontentloaded" });
  const input = page.locator("textarea.composer__input:not([aria-hidden=true])");
  await input.waitFor();
  await selectSession(page, "bench:small-6t");
  await page.waitForFunction(() => document.querySelector(".transcript")?.textContent?.includes("ASYNC LAYOUT EXPANSION COMPLETE"));
  await input.fill("B draft remains here while A asks");
  await input.focus();
  const selected = await readActiveSessionLabel(page);
  await page.evaluate(async () => {
    const { app } = await import("/src/lib/bridge.ts");
    const { installDesktopHostStub } = await import("/src/__tests__/desktopHostStub.ts");
    const { DESKTOP_COMMANDS } = await import("/src/generated/desktopContract.generated.ts");
    const { runtimeStateStore } = await import("/src/lib/runtimeStateStore.ts");
    const { setAttentionPreference } = await import("/src/lib/sound.ts");
    const fallback = Object.fromEntries(DESKTOP_COMMANDS.map(key => [key, app[key]]));
    const readRuntime = async () => { window.__attentionReads++; return runtimeStateStore.getSnapshot(); };
    window.__attentionReads = 0;
    // Focus and periodic reads must use the same producer as the injected
    // frames, including while the notification module is held at its gate.
    installDesktopHostStub({ ...fallback, SyncRuntimeState: readRuntime, GetRuntimeStateSnapshot: readRuntime });
    setAttentionPreference("synth");
    window.__attentionContexts = [];
    const NativeAudioContext = window.AudioContext;
    window.AudioContext = class extends NativeAudioContext {
      constructor(...args) { super(...args); window.__attentionContexts.push(this); }
    };
    let revision = 0;
    window.__publishAttention = (kind = "ask", turnId = "turn-background-A") => {
      const state = { schemaVersion: 1, runtimeEpoch: "runtime-A", activityRevision: 1, revision: ++revision,
        phase: "executing", running: true, turnId, turnStatus: "in_progress", turnEventSeq: 1, pendingPrompt: true,
        pendingInteractions: [{ requestId: "1", kind, turnId, runtimeEpoch: "runtime-A", headId: "head-A" }],
        cancelRequested: false, cancellable: true, backgroundJobs: 0, activity: "" };
      runtimeStateStore.commit({ epoch: "attention-fixture", revision, sessions: [{ tabId: "detached:A", scope: "global",
        workspaceRoot: "/fixture", topicId: "topic-A", sessionId: "session-A", sessionPath: "/fixture/a", sessionGeneration: 1,
        open: false, remote: false, freshness: "synced", state }], topics: [{ scope: "global", node: {
        key: "session-B", topicId: "topic-A", kind: "global_session", label: "Conversation B",
        session: { hostId: "local", sessionId: "session-B" },
      } }, { scope: "global", node: {
        key: "topic-A", topicId: "topic-A", kind: "global_session", label: "Conversation A", status: "waiting_confirmation",
        session: { hostId: "local", sessionId: "session-A" },
      } }] });
    };
    window.__publishAttention();
  });
  assert.equal(await page.evaluate(() => window.__attentionContexts.length), 0, "fixture holds the notification module until a background prompt is pending");
  await page.evaluate(() => { window.__attentionReads = 0; window.dispatchEvent(new Event("focus")); });
  await page.waitForFunction(() => window.__attentionReads > 0);
  assert.equal(await page.evaluate(async () => (await import("/src/lib/runtimeStateStore.ts")).runtimeStateStore.getFailed()), false, "background prompt remains authoritative across focus synchronization");
  releaseNotificationModule();
  await page.getByText("Conversation A is waiting for your answer", { exact: true }).waitFor();
  assert.equal(await input.inputValue(), "B draft remains here while A asks");
  assert.equal(await readActiveSessionLabel(page), selected);
  assert.equal(await input.evaluate(element => element === document.activeElement), true, "notification cannot steal focus");
  assert.equal(await page.evaluate(() => window.__attentionContexts.length), 1);
  assert.equal(await page.evaluate(() => window.__attentionContexts[0].state === "suspended"), false, "Web Audio must not wait for a tab switch");
  const evidence = process.env.REASONIX_ATTENTION_EVIDENCE ?? "/tmp/reasonix-attention-evidence";
  await fs.mkdir(evidence, { recursive: true });
  await page.screenshot({ path: path.join(evidence, "background-ask.png") });
  await page.evaluate(() => window.__publishAttention());
  assert.equal(await page.evaluate(() => window.__attentionContexts.length), 1, "repeated snapshot is silent");
  await page.evaluate(() => window.__publishAttention("approval", "turn-background-approval"));
  await page.getByText("Conversation A is waiting for your approval", { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => window.__attentionContexts.length), 2);
  await selectSession(page, "bench:geometry");
  await page.waitForFunction(() => document.querySelector(".transcript")?.textContent?.includes("Geometry contract fixture complete."));
  await selectSession(page, "bench:small-6t");
  await page.waitForFunction(() => document.querySelector(".transcript")?.textContent?.includes("ASYNC LAYOUT EXPANSION COMPLETE"));
  assert.equal(await input.inputValue(), "B draft remains here while A asks", "draft survives real navigation");
  await page.evaluate(() => window.__publishAttention("approval", "turn-background-approval"));
  assert.equal(await page.evaluate(() => window.__attentionContexts.length), 2, "navigation must not reset notification identity");
  assert.deepEqual(errors, []);
  console.log("PASS background Ask and approval, immediate Web Audio, visible toast, focus/draft preservation, navigation and dedupe");
} finally {
  releaseNotificationModule?.();
  await browser?.close();
  await server.close();
}
