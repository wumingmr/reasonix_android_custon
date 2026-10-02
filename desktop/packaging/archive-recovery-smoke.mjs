// Real packaged UI: archive -> restart -> Trash restore -> restart -> history.
// Uses a disposable home and a loopback fixture provider; no personal data.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { createServer } from "node:http";
import { mkdtempSync, readFileSync, writeFileSync, rmSync, mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { packagedSmokeEnv } from "./smoke-env.mjs";
import { waitForSmokeCondition } from "./smoke-poll.mjs";

const require = createRequire(new URL("../electron/package.json", import.meta.url));
const { _electron } = require("playwright");
const home = mkdtempSync(join(tmpdir(), "reasonix-archive-recovery-"));
const artifacts = resolve(process.argv[3] || "desktop/electron/artifacts/archive-recovery");
mkdirSync(artifacts, { recursive: true });
const server = createServer(async (req, res) => {
  for await (const chunk of req) { /* drain the bounded local fixture request */ }
  res.writeHead(200, { "Content-Type": "text/event-stream" });
  res.end(`data: ${JSON.stringify({ id: "archive-fixture", choices: [{ index: 0, delta: { content: "ARCHIVE_HISTORY_RETAINED" }, finish_reason: null }] })}\n\n`
    + `data: ${JSON.stringify({ id: "archive-fixture", choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\ndata: [DONE]\n\n`);
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
writeFileSync(join(home, "config.toml"), `default_model = "fixture/model"\n[desktop]\nprovider_access = ["fixture"]\n[[providers]]\nname = "fixture"\nkind = "openai"\nbase_url = "http://127.0.0.1:${server.address().port}/v1"\nmodels = ["model"]\ndefault = "model"\napi_key_env = "ARCHIVE_FIXTURE_KEY"\n`);
let application, page;
const errors = [];
const launch = async () => {
  application = await _electron.launch({ executablePath: join(process.argv[2], "Contents/MacOS/Reasonix"),
    env: { ...packagedSmokeEnv(process.env, home), ARCHIVE_FIXTURE_KEY: "loopback-only" } });
  page = await application.firstWindow();
  page.setDefaultTimeout(30000);
  page.on("pageerror", error => errors.push(error.message));
  await page.waitForFunction(() => Boolean(window.reasonixDesktop));
  await page.waitForFunction(() => !document.querySelector(".boot-shell"));
  assert.equal(await page.evaluate(() => window.reasonixDesktop.contract.protocolVersion), 10);
};
const invoke = (method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
const close = async () => { await application.close(); application = null; };
const recordRegistry = name => writeFileSync(join(artifacts, `${name}.json`), readFileSync(join(home, "desktop/workspace-state-v1.json")));
const active = async () => (await invoke("ListTabs")).find(tab => tab.active);
try {
  await launch();
  const version = await invoke("Version");
  await invoke("EnsureBlankSurface", ["global", ""]);
  const composer = page.locator("textarea").first();
  await composer.fill("ARCHIVE_RESTART_FIXTURE");
  await page.locator(".composer__btn--send").click();
  await page.waitForFunction(() => document.querySelector(".chat-transcript")?.textContent?.includes("ARCHIVE_HISTORY_RETAINED"));
  await waitForSmokeCondition(async () => (await invoke("ListTabs")).every(tab => !tab.running));
  const ref = (await active()).session;
  await invoke("RenameCanonicalSession", [ref, "Archive restart fixture"]);
  const initialTopics = await invoke("ListProjectTopics", [{ scope: "global", workspaceRoot: "", limit: 50 }]);
  const initialTopic = initialTopics.items.find(node => node.session?.sessionId === ref.sessionId);
  assert.ok(initialTopic);
  await invoke("SetTopicPinned", [initialTopic.topicId, true]);
  // Use the actual sidebar archive action; its handler invokes the lifecycle owner.
  const row = page.locator(".project-tree__topic").filter({ has: page.getByText("Archive restart fixture", { exact: true }) }).first();
  await row.waitFor();
  await row.hover();
  const archiveButton = row.locator(".project-tree__topic-action--archive");
  await archiveButton.click();
  await waitForSmokeCondition(async () => (await invoke("GetWorkspaceSnapshot")).archivedSessionIds.includes(ref.sessionId));
  await page.screenshot({ path: join(artifacts, "archived.png") });
  await close();
  await launch();
  assert.ok((await invoke("GetWorkspaceSnapshot")).archivedSessionIds.includes(ref.sessionId));
  // Management navigation uses the real UI, not a component mock.
  const trashButton = page.getByRole("button", { name: /回收站|Trash|垃圾桶/i }).first();
  await trashButton.click();
  await page.getByText("Archive restart fixture", { exact: true }).waitFor();
  const tabsBeforePreview = await invoke("ListTabs");
  await page.locator(".archived-sessions__open:visible").click();
  await page.locator(".archived-sessions__preview").getByText("ARCHIVE_HISTORY_RETAINED", { exact: true }).waitFor();
  assert.deepEqual(await invoke("ListTabs"), tabsBeforePreview, "preview created a writable runtime");
  await page.screenshot({ path: join(artifacts, "trash-after-restart.png") });
  await page.locator(".archived-sessions__row:visible").getByRole("button", { name: /恢复|Restore|還原/i }).click();
  await waitForSmokeCondition(async () => !(await invoke("GetWorkspaceSnapshot")).archivedSessionIds.includes(ref.sessionId));
  await page.waitForFunction(() => document.querySelector(".chat-transcript")?.textContent?.includes("ARCHIVE_HISTORY_RETAINED"));
  assert.equal((await active()).session.sessionId, ref.sessionId);
  await close();
  await launch();
  await page.locator(".project-tree__topic-main").filter({ hasText: "Archive restart fixture" }).first().click();
  await page.waitForFunction(() => document.querySelector(".chat-transcript")?.textContent?.includes("ARCHIVE_HISTORY_RETAINED"));
  const history = await invoke("ReadSessionHistory", [ref, "", 32]);
  assert.ok(JSON.stringify(history).includes("ARCHIVE_HISTORY_RETAINED"));
  assert.equal((await active()).session.sessionId, ref.sessionId);
  const listStarted = performance.now();
  const topics = await invoke("ListProjectTopics", [{ scope: "global", workspaceRoot: "", limit: 50 }]);
  const listLatencyMS = Math.round((performance.now() - listStarted) * 100) / 100;
  const containsSession = nodes => nodes.some(node => node.session?.sessionId === ref.sessionId || containsSession(node.children || []));
  assert.ok(containsSession(topics.items));
  const restoredTopic = topics.items.find(node => node.session?.sessionId === ref.sessionId);
  assert.equal(restoredTopic?.topicId, initialTopic.topicId);
  assert.equal(restoredTopic?.pinned, true);
  const diagnostics = await invoke("GetSessionArchitectureDiagnostics");
  writeFileSync(join(artifacts, "recovery-diagnostics.json"), JSON.stringify(await invoke("ListRecoveryEntries", ["", "", 50]), null, 2));
  for (const key of ["pending_operations", "missing_members", "identity_mismatches", "source_conflicts", "recovery_entries"]) {
    assert.equal(diagnostics[key], 0, `clean fixture has unexpected ${key}`);
  }
  assert.deepEqual(errors, []);
  await page.screenshot({ path: join(artifacts, "restored-after-second-restart.png") });
  const restoredRow = page.locator(".project-tree__topic").filter({ has: page.getByText("Archive restart fixture", { exact: true }) }).first();
  await restoredRow.hover();
  await restoredRow.locator(".project-tree__topic-action--archive").click();
  await waitForSmokeCondition(async () => (await invoke("GetWorkspaceSnapshot")).archivedSessionIds.includes(ref.sessionId));
  await page.getByRole("button", { name: /回收站|Trash|垃圾桶/i }).first().click();
  await page.locator(".archived-sessions__delete:visible").click();
  await page.getByRole("dialog").getByRole("button", { name: /彻底删除|Permanently delete|徹底刪除/i }).click();
  await waitForSmokeCondition(async () => (await invoke("ListTrashEntries", ["", "", 50])).items.length === 0);
  recordRegistry("purge-observed");
  await page.screenshot({ path: join(artifacts, "purged.png") });
  await close();
  recordRegistry("purge-closed");
  await launch();
  recordRegistry("purge-restarted");
  assert.equal((await invoke("ListTrashEntries", ["", "", 50])).items.length, 0);
  assert.equal(containsSession((await invoke("ListProjectTopics", [{ scope: "global", workspaceRoot: "", limit: 50 }])).items), false);
  await assert.rejects(invoke("ReadSessionHistory", [ref, "", 32]));
  await close();
  writeFileSync(join(artifacts, "result.json"), JSON.stringify({ passed: true, version, protocol: 10, listLatencyMS, diagnostics, checks: ["real sidebar archive", "persisted archive after restart", "read-only preview", "real Trash restore", "same SessionRef", "history after second restart", "confirmed purge", "no resurrection after restart", "clean exit"] }, null, 2));
  console.log("PASS packaged archive/restart/Trash restore/restart");
} catch (error) {
  if (page && !page.isClosed()) {
    writeFileSync(join(artifacts, "failure-state.json"), JSON.stringify({
      trash: await invoke("ListTrashEntries", ["", "", 50]).catch(() => null),
      workspace: await invoke("GetWorkspaceSnapshot").catch(() => null),
      topics: await invoke("ListProjectTopics", [{ scope: "global", workspaceRoot: "", limit: 50 }]).catch(() => null),
      tree: await invoke("GetProjectTreeSnapshot").catch(() => null),
      tabs: await invoke("ListTabs").catch(() => null),
    }, null, 2));
    await page.screenshot({ path: join(artifacts, "failure.png") }).catch(() => {});
    writeFileSync(join(artifacts, "failure.txt"), `${error.stack}\n${await page.locator("body").innerText().catch(() => "")}`);
  }
  throw error;
} finally {
  if (application) await application.close();
  await new Promise(resolve => server.close(resolve));
  rmSync(home, { recursive: true, force: true });
}
