// Development Electron shell + real Go service, isolated state, no provider.
// Native dialog responses are controlled; renderer actions and persistence are real.
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { fileURLToPath } from "node:url";
import { _electron } from "playwright";
import { setTimeout as delay } from "node:timers/promises";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const home = mkdtempSync(join(tmpdir(), "reasonix-topic-removal-"));
const workspace = join(home, "workspace");
mkdirSync(workspace);
const report = { home, mode: "development shell and service; controlled native confirmation", checks: [], errors: [] };
const check = name => { report.checks.push(name); console.log(`PASS ${name}`); };
let shell, page;
const invoke = (method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
async function poll(read, matches) {
  const deadline = Date.now() + 60000;
  for (;;) {
    const value = await read();
    if (matches(value)) return value;
    if (Date.now() >= deadline) throw new Error("Durable RPC condition timed out");
    await delay(100);
  }
}
async function launch() {
  shell = await _electron.launch({ args: [root], env: { ...process.env,
    REASONIX_HOME: home, REASONIX_STATE_HOME: home, REASONIX_CACHE_HOME: join(home, "cache"),
    REASONIX_DEV: "1", REASONIX_DESKTOP_SERVICE: resolve(root, "../build/bin/reasonix-desktop-service"),
  }, timeout: 60000 });
  page = await shell.firstWindow();
  page.setDefaultTimeout(30000);
  // A fresh isolated profile may open provider setup. Return through its real
  // Back button before operating the draft behind it; no provider is needed.
  await page.addLocatorHandler(page.locator(".settings-screen[data-app-overlay] .management-screen__back"), async back => { await back.click(); });
  page.on("pageerror", error => report.errors.push(error.message));
  await page.waitForFunction(() => Boolean(window.reasonixDesktop) && !document.querySelector(".boot-shell"), null, { timeout: 60000 });
  await shell.evaluate(({ dialog }) => {
    globalThis.__topicRemovalDialogs = [];
    globalThis.__topicRemovalAccept = false;
    dialog.showMessageBox = async (...args) => {
      const options = args.at(-1);
      globalThis.__topicRemovalDialogs.push(options);
      return { response: globalThis.__topicRemovalAccept ? options.buttons.findIndex((_, index) => index !== options.cancelId) : options.cancelId, checkboxChecked: false };
    };
  });
  const deadline = Date.now() + 60000;
  for (;;) {
    try { await invoke("Version"); break; }
    catch (error) { if (Date.now() >= deadline) throw error; await delay(100); }
  }
  console.log("Native service ready");
}
async function openDraft(scope, workspaceRoot) {
  const draft = await invoke("OpenSessionDraftForTarget", [scope, workspaceRoot]);
  await invoke("SetSessionDraftRestoreTarget", [draft.id]);
  await page.reload();
  await page.locator(".draft-topicbar-actions button").waitFor();
  return draft;
}
async function waitDiscarded(id) {
  await poll(() => invoke("GetSessionDraft", [id]), draft => draft.status === "discarded");
}
try {
  await launch();
  await invoke("Version");
  const tabsBefore = await invoke("ListTabs");
  await page.locator(".sidebar__quick-action").click();
  await page.locator(".draft-topicbar-actions button").waitFor();
  const empty = await invoke("RestoreSessionDraft");
  assert.ok(empty?.id, "global quick-create must persist a draft identity");
  await page.locator(".draft-topicbar-actions button").click();
  await waitDiscarded(empty.id);
  assert.equal(await shell.evaluate(() => globalThis.__topicRemovalDialogs.length), 0);
  check("empty draft discarded through real renderer without confirmation");

  const draft = await openDraft("project", workspace);
  const composer = page.locator("textarea.composer__input:not([aria-hidden=true])");
  await composer.fill("Keep this draft until confirmed");
  await page.evaluate(() => window.__reasonixFlushSessionDraft?.());
  await page.locator(".sidebar__quick-action").click();
  const projectFolder = page.locator(".project-tree__folder--project").first();
  const projectCreate = projectFolder.locator(".project-tree__folder-action--create");
  await projectCreate.focus();
  await projectCreate.press("Enter");
  await poll(() => invoke("RestoreSessionDraft"), current => current?.id === draft.id);
  assert.equal(await composer.inputValue(), "Keep this draft until confirmed");
  check("project create entry returns to the saved draft after global navigation");
  await page.locator(".draft-topicbar-actions button").click();
  await page.waitForFunction(() => !document.querySelector(".draft-topicbar-actions button")?.disabled);
  assert.equal((await invoke("GetSessionDraft", [draft.id])).status, "active");
  assert.equal(await composer.inputValue(), "Keep this draft until confirmed");
  check("cancelled confirmation preserves edited draft");
  await shell.evaluate(() => { globalThis.__topicRemovalAccept = true; });
  await page.locator(".draft-topicbar-actions button").click();
  await waitDiscarded(draft.id);
  assert.equal((await invoke("ListTrashEntries", ["", "", 50])).items.length, 0);
  assert.equal((await invoke("ListTabs")).length, tabsBefore.length);
  check("confirmed draft discard creates neither formal session nor trash entry");

  const disposable = await invoke("CreateTopic", ["global", "", ""]);
  let inspection = await invoke("InspectTopicRemoval", [{ workspaceId: "", topicId: disposable.id }]);
  assert.equal(inspection.disposition, "discard_placeholder");
  assert.ok((await invoke("RemoveTopic", [{ operationId: "empty-placeholder", target: inspection.target, expectedToken: inspection.token }])).committed);
  assert.equal((await invoke("ListTrashEntries", ["", "", 50])).items.length, 0);
  check("legacy default placeholder removed without trash");

  const named = await invoke("CreateTopic", ["global", "", "Restorable empty plan"]);
  inspection = await invoke("InspectTopicRemoval", [{ workspaceId: "", topicId: named.id }]);
  await page.reload();
  const globalFolder = page.locator(".project-tree__folder--global .project-tree__folder-main");
  await globalFolder.waitFor();
  if (await globalFolder.getAttribute("aria-expanded") === "false") await globalFolder.click();
  const rowToRemove = page.locator(".project-tree__topic").filter({ hasText: "Restorable empty plan" }).first();
  await rowToRemove.waitFor();
  await rowToRemove.locator(".project-tree__topic-main").click({ button: "right" });
  await page.getByRole("menuitem").last().click();
  await poll(() => invoke("ListTrashEntries", ["", "", 50]), page => page.items.some(row => row.title === named.title));
  let trash = await invoke("ListTrashEntries", ["", "", 50]);
  assert.equal(trash.items.length, 1);
  assert.equal(trash.items[0].canPreview, false);
  const request = { operationId: trash.items[0].recoveryEntryId.replace(/^topic-removal:/, ""), target: inspection.target, expectedToken: inspection.token };
  check("real sidebar action archives named empty topic with a restorable receipt");
  await page.screenshot({ path: join(home, "removed.png") });
  await shell.close(); shell = null;
  await launch();
  assert.equal((await invoke("GetSessionDraft", [draft.id])).status, "discarded");
  trash = await invoke("ListTrashEntries", ["", "", 50]);
  const row = trash.items.find(row => row.title === named.title);
  assert.ok(row?.canRestore);
  const result = await invoke("ApplySessionLifecycle", [{ operationId: "restore-placeholder", action: "restore", expectedGeneration: trash.generation,
    targets: [{ workspaceId: row.workspaceId, recoveryEntryId: row.recoveryEntryId }] }]);
  assert.ok(result.committed);
  assert.ok((await invoke("ListProjectTopics", [{ scope: "global", workspaceRoot: "", limit: 50 }])).items.some(row => row.topicId === named.id));
  assert.ok((await invoke("RemoveTopic", [request])).committed);
  assert.ok((await invoke("ListProjectTopics", [{ scope: "global", workspaceRoot: "", limit: 50 }])).items.some(row => row.topicId === named.id));
  check("restart preserves discard and recoverable metadata; late duplicate cannot remove restored topic");
  assert.deepEqual(report.errors, []);
} catch (error) {
  report.failure = String(error);
  if (page && !page.isClosed()) await page.screenshot({ path: join(home, "failure.png") }).catch(() => {});
  console.error(error);
  throw error;
} finally {
  if (shell) {
    const deadline = setTimeout(() => shell.process().kill("SIGKILL"), 15000);
    try { await shell.close().catch(() => {}); } finally { clearTimeout(deadline); }
  }
  writeFileSync(join(home, "results.json"), JSON.stringify(report, null, 2));
  console.log(`Evidence: ${join(home, "results.json")}`);
}
