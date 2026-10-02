// Production macOS package: original JSONL/event history stays readable when
// an external writer prevents execution recovery. Fixtures never call a model.
// Usage: node desktop/packaging/history-native-smoke.mjs /path/Reasonix.app
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { createRequire } from "node:module";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { packagedSmokeEnv } from "./smoke-env.mjs";
import { parseServiceReady, waitForSmokeCondition } from "./smoke-poll.mjs";
import { closeAndVerify } from "./smoke-lifecycle.mjs";

assert.equal(process.platform, "darwin", "This fixture exercises the macOS production bundle");
const require = createRequire(new URL("../electron/package.json", import.meta.url));
const { _electron } = require("playwright");
const bundle = resolve(process.argv[2]);
const home = mkdtempSync(join(tmpdir(), "reasonix-native-history-"));
const dir = join(home, "sessions");
mkdirSync(dir);
writeFileSync(join(home, "config.toml"), `default_model = "fixture/model"\n[desktop]\nprovider_access = ["fixture"]\n[[providers]]\nname = "fixture"\nkind = "openai"\nbase_url = "http://127.0.0.1:1/v1"\nmodels = ["model"]\ndefault = "model"\napi_key_env = "HISTORY_FIXTURE_KEY"\n`);
const answer = "完整内容🧭".repeat(90000);
const fixtures = ["checkpoint", "events"].map(kind => {
  const messages = Array.from({ length: 140 }, (_, index) => [
    { role: "user", content: `NATIVE_${kind}_QUESTION_${index}` },
    { role: "assistant", content: index === 139 ? answer : `NATIVE_${kind}_ANSWER_${index}` },
  ]).flat();
  const path = join(dir, `${kind}.jsonl`);
  const eventPath = join(dir, `${kind}.events.jsonl`);
  const body = kind === "checkpoint" ? messages.map(message => JSON.stringify(message)).join("\n") + "\n"
    : JSON.stringify({ role: "user", content: "OBSOLETE_CHECKPOINT" }) + "\n";
  writeFileSync(path, body);
  const events = JSON.stringify({ messages, type: "replace", schema_version: 1 }) + "\n";
  if (kind === "events") writeFileSync(eventPath, events);
  return { kind, path, eventPath, body, events };
});
let application, lease, page;
let passed = false;
try {
  // The OS lock is the real writer fence. No service/runtime test hook is used.
  lease = spawn("python3", ["-u", "-c", `import fcntl, sys
locks = [open(path, 'a+') for path in sys.argv[1:]]
for file in locks: fcntl.flock(file, fcntl.LOCK_EX | fcntl.LOCK_NB)
print('locked', flush=True)
sys.stdin.read()
`, ...fixtures.map(fixture => fixture.path + ".lease.lock")], { stdio: ["pipe", "pipe", "inherit"] });
  await Promise.race([once(lease.stdout, "data").then(([value]) => assert.match(String(value), /locked/)),
    once(lease, "exit").then(() => { throw new Error("fixture writer exited before lock acquisition"); })]);
  application = await _electron.launch({ executablePath: join(bundle, "Contents/MacOS/Reasonix"),
    env: { ...packagedSmokeEnv(process.env, home), HISTORY_FIXTURE_KEY: "local-fixture" } });
  await waitForSmokeCondition(async () => {
    for (const candidate of application.windows()) {
      if (await candidate.evaluate(() => Boolean(window.reasonixDesktop)).catch(() => false)) { page = candidate; return true; }
    }
    return false;
  });
  const invoke = (method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
  const build = JSON.parse(readFileSync(join(bundle, "Contents/Resources/build.json"), "utf8"));
  assert.equal(await invoke("Version"), build.version);
  assert.equal(await application.evaluate(({ app }) => app.isPackaged && !process.env.REASONIX_DEV), true);
  const folder = page.locator(".project-tree__folder-main").first();
  await folder.waitFor();
  if (await folder.getAttribute("aria-expanded") !== "true") await folder.click();
  for (const fixture of fixtures) {
    await page.locator(".project-tree__topic-main").filter({ has: page.getByText(fixture.kind, { exact: true }) }).click();
    let selected;
    await waitForSmokeCondition(async () => {
      selected = (await invoke("ListTabs")).find(tab => tab.active && tab.sessionPath === fixture.path);
      return Boolean(selected);
    });
    const ticket = { tabId: selected.id };
    await page.waitForFunction(kind => document.querySelector(".chat-transcript")?.textContent?.includes(`NATIVE_${kind}_QUESTION_139`), fixture.kind);
    const visible = await page.locator(".chat-transcript").innerText();
    for (const other of fixtures.filter(other => other !== fixture)) {
      assert.equal(visible.includes(`NATIVE_${other.kind}_QUESTION_`), false, "previous session remained visible");
    }
    const composer = page.locator("#composer-input");
    await composer.fill("Unsaved draft while the external writer owns execution");
    assert.equal(await page.locator(".composer__btn--send").isEnabled(), false);
    await composer.fill("");
    const handle = await invoke("BeginSessionHistoryReadForTab", [ticket.tabId]);
    assert.equal(handle.storageBackend, "legacy");
    const newest = await invoke("ReadSessionHistorySlice", [handle.id, { entries: 32, turns: 32, bytes: 1 << 20 }]);
    assert.equal(newest.status, "ready");
    assert.equal(newest.page.hasOlder, true);
    assert.equal(newest.page.entries.length <= 32, true);
    const outline = await invoke("ReadSessionHistoryOutline", [handle.id, { limit: 128 }]);
    assert.equal(outline.status, "ready");
    assert.equal(outline.entries.length, 128);
    assert.equal(outline.totalTurns, 140);
    const target = outline.entries[3];
    const location = await invoke("LocateSessionHistoryMessage", [handle.id, target.messageId, outline.snapshotSequence]);
    assert.equal(location.status, "ready");
    const located = await invoke("ReadSessionHistorySlice", [handle.id, { anchor: "turn", turn: 4, entries: 2, generation: outline.generation, snapshotSequence: outline.snapshotSequence }]);
    assert.equal(located.status, "ready");
    assert.equal(located.page.entries.at(-1).message.content, `NATIVE_${fixture.kind}_QUESTION_3`);
    assert.ok(handle.capabilities.includes("history-native-search-v1"));
    let search;
    await waitForSmokeCondition(async () => {
      search = await invoke("SearchSessionHistoryRead", [handle.id, `NATIVE_${fixture.kind}_QUESTION_`, "", 2]);
      assert.ok(["preparing", "ready"].includes(search.status));
      if (search.status === "preparing") assert.equal(search.hits.length, 0);
      return search.status === "ready";
    });
    assert.equal(search.hits.length, 2);
    assert.equal(search.hasMore, true);
    assert.equal(search.hits[0].preview, `NATIVE_${fixture.kind}_QUESTION_139`);
    const searchLocation = await invoke("LocateSessionHistoryMessage", [handle.id, search.hits[0].messageId, search.snapshotSequence]);
    assert.equal(searchLocation.status, "ready");
    const nextSearch = await invoke("SearchSessionHistoryRead", [handle.id, `NATIVE_${fixture.kind}_QUESTION_`, search.nextCursor, 2]);
    assert.equal(nextSearch.status, "ready");
    assert.equal(nextSearch.hits[0].preview, `NATIVE_${fixture.kind}_QUESTION_137`);
    const ref = newest.page.entries.flatMap(entry => entry.refs ?? []).find(ref => ref.field === "content");
    assert.equal(ref?.readHandleId, handle.id);
    let text = "";
    for (let index = 0; ; index++) {
      const chunk = await invoke("HistoryContentForTab", [ticket.tabId, ref, index]);
      assert.equal(chunk.stale, false);
      text += chunk.data;
      if (chunk.done) break;
    }
    assert.equal(text, answer);
    await invoke("ReleaseSessionHistoryRead", [handle.id]);
    assert.equal((await invoke("HistoryContentForTab", [ticket.tabId, ref, 0])).stale, true);
    const reopen = await invoke("BeginSessionHistoryReadForTab", [ticket.tabId]);
    assert.equal((await invoke("ReadSessionHistorySlice", [reopen.id, { entries: 2 }])).status, "ready");
    assert.equal((await invoke("HistoryContentForTab", [ticket.tabId, ref, 0])).stale, true);
    await invoke("ReleaseSessionHistoryRead", [reopen.id]);
    const compatibility = await invoke("HistorySliceForTab", [ticket.tabId, { entries: 2 }]);
    const compatibilityRef = compatibility.entries.flatMap(entry => entry.refs ?? []).find(ref => ref.field === "content");
    assert.ok(compatibilityRef, JSON.stringify({ source: compatibility.source, error: compatibility.error,
      stale: compatibility.stale, entries: compatibility.entries.map(entry => ({ id: entry.id, refs: entry.refs?.length })) }));
    assert.equal(compatibilityRef.readHandleId ?? "", "");
    const compatibilityChunk = await invoke("HistoryContentForTab", [ticket.tabId, compatibilityRef, 0]);
    assert.equal(compatibilityChunk.stale, false);
    assert.ok(compatibilityChunk.data && answer.startsWith(compatibilityChunk.data));
    await waitForSmokeCondition(async () => (await invoke("ListTabs")).find(tab => tab.id === ticket.tabId)?.runtime?.phase === "lease_blocked");
    assert.equal(readFileSync(fixture.path, "utf8"), fixture.body);
    if (fixture.kind === "events") assert.equal(readFileSync(fixture.eventPath, "utf8"), fixture.events);
    assert.equal(existsSync(fixture.path.replace(/\.jsonl$/, ".display-index.json")), false);
    console.log(`PASS ${fixture.kind}: bounded pages, outline, direct anchor, cold search and hit location, complete Unicode content, released refs fenced, unbound content, writer conflict; source unchanged`);
  }
  await waitForSmokeCondition(async () => {
    const topics = await invoke("ListProjectTopics", [{ scope: "global", limit: 50 }]);
    assert.equal(topics.failedDirectories, 0, "optional absent history roots reported as failed");
    return topics.complete && topics.pendingDirectories === 0;
  });
  const workspace = { scope: "global" };
  let organization = await invoke("GetSessionOrganization", [workspace]);
  const mutate = async mutation => {
    // Match the production client's CAS contract: first-time legacy preference
    // import may advance the revision before the requested mutation is applied.
    for (let attempt = 0; attempt < 3; attempt++) {
      organization = await invoke("UpdateSessionOrganization", [workspace, organization.revision, mutation]);
      if (organization.applied) return;
    }
    assert.fail("organization mutation could not settle under the production CAS contract");
  };
  await mutate({ kind: "create-group", groupId: "native-fixtures", title: "Native fixtures" });
  const sources = (await invoke("ListProjectTopics", [{ scope: "global", limit: 50 }])).items
    .filter(node => fixtures.some(fixture => fixture.path === node.sessionPath));
  assert.equal(sources.length, 2);
  for (const node of sources) await mutate({ kind: "set-group", groupId: "native-fixtures", target: { source: node.source } });
  const filtered = await invoke("ListProjectTopics", [{ scope: "global", groupFilter: "group", groupId: "native-fixtures", query: "CHECKPOINT", limit: 1 }]);
  assert.equal(filtered.items.length, 1);
  assert.equal(filtered.items[0].sessionPath, fixtures.find(fixture => fixture.kind === "checkpoint").path);
  assert.equal(filtered.nextCursor ?? "", "");
  await invoke("ReleaseReadSnapshot", [filtered.snapshotId]);
  const groupedRequest = { scope: "global", groupFilter: "group", groupId: "native-fixtures", limit: 1 };
  const first = await invoke("ListProjectTopics", [groupedRequest]);
  assert.equal(first.items.length, 1);
  assert.ok(first.nextCursor);
  const removed = sources.find(node => node.sessionPath !== first.items[0].sessionPath);
  await mutate({ kind: "set-group", groupId: "", target: { source: removed.source } });
  const retained = await invoke("ListProjectTopics", [{ ...groupedRequest, cursor: first.nextCursor }]);
  assert.equal(retained.items.length, 1);
  assert.equal(retained.items[0].sessionPath, removed.sessionPath);
  assert.equal(retained.nextCursor ?? "", "");
  await invoke("ReleaseReadSnapshot", [first.snapshotId]);
  const fresh = await invoke("ListProjectTopics", [groupedRequest]);
  assert.equal(fresh.items.length, 1);
  assert.equal(fresh.items[0].sessionPath, first.items[0].sessionPath);
  assert.equal(fresh.nextCursor ?? "", "");
  await invoke("ReleaseReadSnapshot", [fresh.snapshotId]);
  console.log("PASS native group paging: source membership, text filtering, retained cursor, refreshed membership");
  const ready = parseServiceReady(readFileSync(join(home, "desktop-shell/logs/shell.log"), "utf8"));
  assert.ok(ready);
  await closeAndVerify(application, { shellPid: await application.evaluate(() => process.pid), servicePid: ready.pid });
  application = undefined;
  console.log(`PASS production history ${build.version} ${build.commit ?? ""}: normal shell/service exit`);
  passed = true;
} catch (error) {
  if (page) {
    await page.screenshot({ path: join(home, "failure.png"), fullPage: true }).catch(() => {});
    const state = await page.evaluate(async () => ({ body: document.body.innerText,
      tabs: await window.reasonixDesktop.invoke("ListTabs", []),
      tree: await window.reasonixDesktop.invoke("GetProjectTreeSnapshot", []),
      topics: await window.reasonixDesktop.invoke("ListProjectTopics", [{ scope: "global", limit: 50 }]),
    })).catch(error => ({ error: String(error) }));
    writeFileSync(join(home, "failure.json"), JSON.stringify(state, null, 2));
  }
  throw error;
} finally {
  await application?.close();
  if (lease && lease.exitCode === null) { const exited = once(lease, "exit"); lease.stdin.end(); await exited; }
  if (passed) rmSync(home, { recursive: true, force: true });
  else console.error(`Native history evidence retained at ${home}`);
}
