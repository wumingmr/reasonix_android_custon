// Native shell + bundled Go host; every home is disposable and synthetic.
import assert from "node:assert/strict";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { _electron as electron } from "playwright";

const executablePath = process.argv[2];
assert(executablePath, "usage: node tagged-history-smoke.mjs <packaged executable> [versions...]");
const versions = process.argv.slice(3);
if (!versions.length) for (let n = 3; n <= 11; n++) versions.push(`1.38.${n}`);
const fixtures = fileURLToPath(new URL("../../testdata/session-history-upgrade/", import.meta.url));
const evidence = process.env.REASONIX_HISTORY_EVIDENCE || "/tmp/reasonix-tagged-package-evidence";
mkdirSync(evidence, { recursive: true });
const results = [];
const rpc = (page, command, ...args) => page.evaluate(({ command, args }) => window.reasonixDesktop.invoke(command, args), { command, args });
const pause = () => new Promise(resolve => setTimeout(resolve, 100));

for (const version of versions) {
  const fixture = path.join(fixtures, version);
  const source = JSON.parse(readFileSync(path.join(fixture, "SOURCE.json"), "utf8"));
  const home = mkdtempSync(path.join(tmpdir(), `reasonix-tagged-${version}-`));
  cpSync(path.join(fixture, "legacy"), path.join(home, "sessions"), { recursive: true });
  if (existsSync(path.join(fixture, "canonical"))) cpSync(path.join(fixture, "canonical"), path.join(home, "sessions-v4"), { recursive: true });
  const env = { ...process.env, REASONIX_HOME: home, REASONIX_STATE_HOME: home, REASONIX_CACHE_HOME: path.join(home, "cache") };
  delete env.REASONIX_DEV;
  delete env.REASONIX_DESKTOP_SERVICE;
  let shell;
  async function launch() {
    shell = await electron.launch({ executablePath, env, timeout: 60000 });
    const page = await shell.firstWindow();
    await page.waitForURL("reasonix://app/index.html", { timeout: 60000 });
    await page.locator(".app").waitFor({ state: "visible", timeout: 60000 });
    await page.addLocatorHandler(page.locator(".management-screen__back:visible"), async button => button.click());
    await page.locator(".sidebar__quick-action:visible").first().click({ trial: true, timeout: 60000 });
    return page;
  }
  try {
    let page = await launch();
    const initial = await rpc(page, "ListHistoricalSessions");
    const count = Number(version.split(".")[2]) >= 8 ? 6 : 3;
    assert.equal(initial.items.length, count, JSON.stringify(initial));
    assert(initial.items.every(item => !item.session), "startup must preserve explicit historical import");
    await rpc(page, "StartHistoricalImport", initial.items.map(item => item.id));
    let status;
    const deadline = Date.now() + 120000;
    do { await pause(); status = await rpc(page, "ListHistoricalSessions"); } while (status.running && Date.now() < deadline);
    assert.equal(status.running, false, "historical import did not settle");
    assert.equal(status.failed, 0, JSON.stringify(status));
    assert.equal(status.blocked, 0, JSON.stringify(status));
    const refs = status.items.map(item => item.session);
    assert(refs.every(Boolean), JSON.stringify(status));
    assert.equal(new Set(refs.map(ref => ref.sessionId)).size, count);
    for (const ref of refs) {
      const history = await rpc(page, "ReadSessionHistory", ref, "", 100);
      assert(history.messages.some(message => /Answer 69|Independent branch work/.test(message.content)), "historical tail missing");
      const state = await rpc(page, "GetSessionComposerState", ref);
      await rpc(page, "SaveSessionComposerState", { ref, expectedRevision: state.revision, contentVersion: 1, contentJson: JSON.stringify({ text: `unsent ${version} ${ref.sessionId}` }) });
    }
    await shell.close(); shell = undefined;
    page = await launch();
    for (const ref of refs) {
      const restored = await rpc(page, "GetSessionComposerState", ref);
      assert.equal(JSON.parse(restored.contentJson).text, `unsent ${version} ${ref.sessionId}`);
      assert(!restored.historyChanged, "unchanged history should not require stale-input review");
    }
    const snapshot = await rpc(page, "GetWorkspaceSnapshot");
    assert.deepEqual([...new Set(snapshot.workspaces.flatMap(workspace => workspace.sessionIds))].sort(), refs.map(ref => ref.sessionId).sort(), "restart must not create another session");
    assert.deepEqual(await rpc(page, "ListSessionDraftSummaries"), []);
    for (const [name, expected] of Object.entries(source.files)) {
      // Startup may refresh rebuildable display/index timestamps. The durable
      // transcript, event log, metadata and content objects remain immutable.
      if (name.endsWith(".display-index.json") || name.endsWith(".event-index.json")) continue;
      const relative = name.startsWith("legacy/") ? name.replace("legacy/", "sessions/") : name.startsWith("canonical/") ? name.replace("canonical/", "sessions-v4/") : undefined;
      if (!relative) continue;
      assert.equal(createHash("sha256").update(readFileSync(path.join(home, relative))).digest("hex"), expected, `original source modified: ${name}`);
    }
    await page.screenshot({ path: path.join(evidence, `${version}.png`) });
    results.push({ version, commit: source.commit, home, sessions: count, refs, status: "passed" });
    writeFileSync(path.join(evidence, "results.json"), JSON.stringify({ executablePath, results }, null, 2));
    console.log(`PASS packaged ${version}: ${count} historical sessions, restart, unsent input, unchanged source`);
  } catch (error) {
    if (shell) await (await shell.firstWindow()).screenshot({ path: path.join(evidence, `${version}-failure.png`) });
    throw error;
  } finally { if (shell) await shell.close(); }
}
