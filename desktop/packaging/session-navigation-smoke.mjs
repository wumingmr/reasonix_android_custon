// Real packaged sidebar -> navigation owner -> service -> transcript regression.
// Usage: node desktop/packaging/session-navigation-smoke.mjs /path/Reasonix.app
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { createServer } from "node:http";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { packagedSmokeEnv } from "./smoke-env.mjs";
import { waitForSmokeCondition } from "./smoke-poll.mjs";

const require = createRequire(new URL("../electron/package.json", import.meta.url));
const { _electron } = require("playwright");
const home = mkdtempSync(join(tmpdir(), "reasonix-sidebar-navigation-"));
const server = createServer(async (req, res) => {
  let raw = "";
  for await (const chunk of req) raw += chunk;
  const request = JSON.parse(raw || "{}");
  const messages = JSON.stringify(request.messages ?? []);
  const marker = messages.includes("NAV_BETA") ? "NAV_BETA" : "NAV_ALPHA";
  res.writeHead(200, { "Content-Type": "text/event-stream" });
  res.end(`data: ${JSON.stringify({ id: "fixture", choices: [{ index: 0, delta: { content: `ANSWER_${marker}` }, finish_reason: null }] })}\n\n`
    + `data: ${JSON.stringify({ id: "fixture", choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\ndata: [DONE]\n\n`);
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
writeFileSync(join(home, "config.toml"), `default_model = "fixture/model"\n[desktop]\nprovider_access = ["fixture"]\n[[providers]]\nname = "fixture"\nkind = "openai"\nbase_url = "http://127.0.0.1:${server.address().port}/v1"\nmodels = ["model"]\ndefault = "model"\napi_key_env = "SIDEBAR_FIXTURE_KEY"\n`);
let application, page;
let failed = false;
try {
  application = await _electron.launch({ executablePath: join(process.argv[2], "Contents/MacOS/Reasonix"),
    env: { ...packagedSmokeEnv(process.env, home), SIDEBAR_FIXTURE_KEY: "local-fixture" } });
  page = await application.firstWindow();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.waitForFunction(() => Boolean(window.reasonixDesktop));
  await page.evaluate(() => {
    window.__navigationSmokeEvents = [];
    window.reasonixDesktop.on("topic:activation", event => {
      window.__navigationSmokeEvents.push(event);
      if (window.__navigationSmokeEvents.length > 64) window.__navigationSmokeEvents.shift();
    });
  });
  const invoke = (method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
  const active = async () => (await invoke("ListTabs")).find(tab => tab.active);
  const transcriptContains = (text, expected = true) => page.waitForFunction(({ text, expected }) =>
    (document.querySelector(".chat-transcript")?.textContent?.includes(text) ?? false) === expected, { text, expected });
  const refs = {};
  await invoke("CreateSession", ["global"]);
  for (const marker of ["NAV_ALPHA", "NAV_BETA"]) {
    console.log("Seeding", marker);
    if (marker === "NAV_BETA") {
      await page.locator(".sidebar__quick-action").click();
      await transcriptContains("ANSWER_NAV_ALPHA", false);
    }
    const composer = page.locator("textarea").first();
    for (let attempt = 0; attempt < 100; attempt++) {
      await composer.fill(marker);
      if (await page.locator(".composer__btn--send").isEnabled()) break;
      await page.waitForTimeout(100);
    }
    await page.locator(".composer__btn--send").click();
    await transcriptContains(`ANSWER_${marker}`);
    await waitForSmokeCondition(async () => (await invoke("ListTabs")).every(tab => !tab.running));
    refs[marker] = (await active()).session;
    await invoke("RenameCanonicalSession", [refs[marker], marker]);
  }
  assert.notEqual(refs.NAV_ALPHA.sessionId, refs.NAV_BETA.sessionId);
  const sessionRow = marker => page.locator(".project-tree__topic-main").filter({ has: page.getByText(marker, { exact: true }) });
  for (const marker of ["NAV_ALPHA", "NAV_BETA", "NAV_ALPHA"]) {
    console.log("Selecting", marker);
    await sessionRow(marker).click();
    await transcriptContains(`ANSWER_${marker}`);
    const other = marker === "NAV_ALPHA" ? "NAV_BETA" : "NAV_ALPHA";
    await transcriptContains(`ANSWER_${other}`, false);
    assert.equal((await active()).session.sessionId, refs[marker].sessionId);
    assert.equal(await sessionRow(marker).locator("xpath=..").evaluate(node => node.classList.contains("project-tree__topic--active")), true);
  }
  await page.evaluate(markers => {
    for (const marker of markers) {
      const label = [...document.querySelectorAll(".project-tree__topic-label")]
        .find(candidate => candidate.textContent?.trim() === marker);
      const row = label?.closest(".project-tree__topic-main");
      if (!row) throw new Error(`missing project tree row ${marker}`);
      row.click();
    }
  }, ["NAV_BETA", "NAV_ALPHA", "NAV_BETA"]);
  await transcriptContains("ANSWER_NAV_BETA");
  await transcriptContains("ANSWER_NAV_ALPHA", false);
  assert.equal((await active()).session.sessionId, refs.NAV_BETA.sessionId);
  assert.deepEqual(errors, []);
  console.log("PASS packaged sidebar: create after completed turn, A/B/A selection and transcript agree, rapid clicks keep the last target; no page errors");
} catch (error) {
  failed = true;
  if (page) {
    const state = await page.evaluate(async () => ({
      tabs: await window.reasonixDesktop.invoke("ListTabs", []),
      topics: await window.reasonixDesktop.invoke("ListProjectTopics", [{ scope: "global", limit: 50 }]).catch(error => ({ error: String(error) })),
      sidebar: document.querySelector(".project-tree")?.textContent,
      transcript: await (async () => {
        const tab = (await window.reasonixDesktop.invoke("ListTabs", [])).find(tab => tab.active);
        if (!tab) return null;
        return window.reasonixDesktop.invoke("TranscriptSnapshotForTab", [tab.id, { records: 32 }]).catch(error => ({ error: String(error) }));
      })(),
      body: document.body.innerText,
      activationEvents: window.__navigationSmokeEvents,
      catalog: await window.reasonixDesktop.invoke("GetProjectTreeSnapshot", []).catch(error => ({ error: String(error) })),
    })).catch(error => ({ error: String(error) }));
    writeFileSync(join(home, "navigation-failure.json"), JSON.stringify(state, null, 2));
    await page.screenshot({ path: join(home, "navigation-failure.png"), fullPage: true }).catch(() => {});
  }
  console.error(`Native navigation evidence retained at ${home}`);
  throw error;
} finally {
  await application?.close();
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
  if (!failed) rmSync(home, { recursive: true, force: true });
}
