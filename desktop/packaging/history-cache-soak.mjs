// Real renderer navigation and cache accounting in an unchanged macOS bundle.
// Usage: node desktop/packaging/history-cache-soak.mjs /path/Reasonix.app
// The bench URL only enables the existing diagnostic hook; all data, RPCs,
// writer fences, and shell/service identity checks remain production paths.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";
import { tmpdir, platform, arch } from "node:os";
import { packagedSmokeEnv } from "./smoke-env.mjs";
import { parseServiceReady, waitForSmokeCondition } from "./smoke-poll.mjs";
import { closeAndVerify } from "./smoke-lifecycle.mjs";

assert.equal(platform(), "darwin");
const bundle = resolve(process.argv[2]);
const require = createRequire(new URL("../electron/package.json", import.meta.url));
const { _electron } = require("playwright");
const home = mkdtempSync(join(tmpdir(), "reasonix-history-cache-"));
const resultPath = `${home}.results.json`;
const dir = join(home, "sessions");
mkdirSync(dir);
writeFileSync(join(home, "config.toml"), `default_model = "fixture/model"\n[desktop]\nprovider_access = ["fixture"]\n[[providers]]\nname = "fixture"\nkind = "openai"\nbase_url = "http://127.0.0.1:1/v1"\nmodels = ["model"]\ndefault = "model"\napi_key_env = "HISTORY_FIXTURE_KEY"\n`);
const hash = path => createHash("sha256").update(readFileSync(path)).digest("hex");
const fixtures = Array.from({ length: 8 }, (_, id) => {
  const name = `cache-${id}`;
  const messages = Array.from({ length: 160 }, (_, turn) => [
    { role: "user", content: `SOAK_${id}_QUESTION_${turn}` },
    { role: "assistant", content: `SOAK_${id}_ANSWER_${turn}\n\n` + `**History ${id}/${turn}** with Unicode 🧭 and inline \`code\`.\n\n`.repeat(12)
      + `\n\`\`\`svg\n<svg xmlns="http://www.w3.org/2000/svg" width="120" height="40"><text x="2" y="25">${id}/${turn}</text></svg>\n\`\`\`\n` },
  ]).flat();
  const path = join(dir, `${name}.jsonl`);
  writeFileSync(path, id % 2 === 0 ? messages.map(message => JSON.stringify(message)).join("\n") + "\n"
    : '{"role":"user","content":"SUPERSEDED_CHECKPOINT"}\n');
  const files = [path];
  if (id % 2) {
    const events = join(dir, `${name}.events.jsonl`);
    writeFileSync(events, JSON.stringify({ messages, type: "replace", schema_version: 1 }) + "\n");
    files.push(events);
  }
  return { id, name, path, files: files.map(path => ({ path, digest: hash(path) })) };
});
const report = { build: JSON.parse(readFileSync(join(bundle, "Contents/Resources/build.json"), "utf8")),
  machine: { platform: platform(), arch: arch() }, complete: false, samples: [],
  scope: "Real UI navigation, bidirectional paging, cache accounting, worker drain and SVG object URL release for checkpoint/schema-1 fixtures; not whole-process memory or idle-runtime qualification." };
let application, page, lease;
const invoke = (method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
const stats = () => page.evaluate(() => window.__reasonixPerf.stats());
async function sample(id, phase) {
  await waitForSmokeCondition(async () => (await stats()).markdownWorker?.pending === 0);
  const value = await stats();
  const cache = value.transcriptCache;
  assert.ok(cache && cache.residentSessions > 0, "real renderer did not populate transcript cache");
  assert.ok(cache.residentSessions <= cache.maxResidentSessions, "resident-session budget exceeded");
  assert.ok(cache.bodyBytes <= cache.bodyBudgetBytes, "body budget exceeded");
  assert.ok(cache.markdownBytes <= cache.markdownBudgetBytes, "Markdown budget exceeded");
  assert.ok(cache.residentWindowEntries <= cache.residentSessions * cache.windowMaxPages * 32, "resident history window exceeded");
  const objectURLs = await page.evaluate(() => ({ ...window.__historyObjectURLs(), mountedSVG: document.querySelectorAll(".md-svg").length }));
  report.samples.push({ fixture: id, phase, cache, markdownWorker: value.markdownWorker, objectURLs });
  assert.ok(objectURLs.active <= objectURLs.mountedSVG, "unmounted SVG retained an object URL");
}
async function select(fixture) {
  await page.locator(".project-tree__topic-main").filter({ has: page.getByText(fixture.name, { exact: true }) }).click();
  await waitForSmokeCondition(async () => (await invoke("ListTabs")).some(tab => tab.active && tab.sessionPath === fixture.path));
  await page.waitForFunction(id => document.querySelector(".chat-transcript")?.textContent?.includes(`SOAK_${id}_QUESTION_`), fixture.id);
  const text = await page.locator(".chat-transcript").innerText();
  for (const other of fixtures.filter(other => other !== fixture)) assert.equal(text.includes(`SOAK_${other.id}_`), false, "previous source remained visible");
  assert.equal(text.includes("SUPERSEDED_CHECKPOINT"), false);
}
async function pageHistory(fixture) {
  // Exercise the same buttons readers use, never call history RPCs directly or
  // mutate scrollTop to bypass the transcript's navigation owner.
  await page.locator(".chat-flow-scroll").hover();
  await page.mouse.wheel(0, -1000000);
  await page.waitForFunction(() => document.querySelector(".chat-flow-scroll")?.getAttribute("data-scroll-mode") === "reader");
  for (let index = 0; index < 4; index++) {
    const before = await page.locator(".chat-history-window").innerText();
    await page.locator(".chat-older").click();
    await page.waitForFunction(previous => {
      const button = document.querySelector(".chat-older");
      return button && !button.disabled && document.querySelector(".chat-history-window")?.innerText !== previous;
    }, before);
    report.samples.push({ fixture: fixture.id, phase: "page", range: await page.locator(".chat-history-window").innerText(), cache: (await stats()).transcriptCache });
  }
  assert.equal((await page.locator(".chat-column").innerText()).includes(`SOAK_${fixture.id}_QUESTION_159`), false, "newest page was not reclaimed");
  await sample(fixture.id, "older");
  await page.locator(".chat-history-newer button").last().click();
  await page.waitForFunction(id => document.querySelector(".chat-column")?.textContent?.includes(`SOAK_${id}_QUESTION_159`), fixture.id);
  await sample(fixture.id, "latest");
}
try {
  lease = spawn("python3", ["-u", "-c", `import fcntl, sys
locks = [open(path, 'a+') for path in sys.argv[1:]]
for file in locks: fcntl.flock(file, fcntl.LOCK_EX | fcntl.LOCK_NB)
print('locked', flush=True)
sys.stdin.read()
`, ...fixtures.map(fixture => fixture.path + ".lease.lock")], { stdio: ["pipe", "pipe", "inherit"] });
  await Promise.race([once(lease.stdout, "data").then(([data]) => assert.match(String(data), /locked/)),
    once(lease, "exit").then(() => { throw new Error("fixture writer exited before locking"); })]);
  application = await _electron.launch({ executablePath: join(bundle, "Contents/MacOS/Reasonix"),
    env: { ...packagedSmokeEnv(process.env, home), HISTORY_FIXTURE_KEY: "local-fixture" } });
  await waitForSmokeCondition(async () => {
    for (const candidate of application.windows()) if (await candidate.evaluate(() => Boolean(window.reasonixDesktop)).catch(() => false)) { page = candidate; return true; }
    return false;
  });
  assert.equal(await invoke("Version"), report.build.version);
  assert.equal(await application.evaluate(({ app }) => app.isPackaged && !process.env.REASONIX_DEV), true);
  const url = new URL(page.url());
  url.searchParams.set("bench", "1");
  // Observe native URL ownership without altering app/cache/RPC behavior.
  // Each navigation gets a fresh counter; only content-free counts are saved.
  await page.addInitScript(() => {
    const active = new Set();
    let created = 0, revoked = 0;
    const create = URL.createObjectURL, revoke = URL.revokeObjectURL;
    URL.createObjectURL = function(blob) {
      const url = create.call(this, blob);
      // Inline worker loaders revoke their JavaScript blob from inside the
      // worker realm. A page-only observer cannot count those releases. Track
      // SVG resources owned by the mounted history components specifically.
      if (blob.type === "image/svg+xml") { active.add(url); created++; }
      return url;
    };
    URL.revokeObjectURL = function(url) {
      if (active.delete(url)) revoked++;
      return revoke.call(this, url);
    };
    window.__historyObjectURLs = () => ({ kind: "image/svg+xml", created, revoked, active: active.size });
  });
  await page.goto(url.href);
  await page.waitForFunction(() => Boolean(window.__reasonixPerf && window.reasonixDesktop));
  assert.equal(await invoke("Version"), report.build.version);
  const folder = page.locator(".project-tree__folder-main").first();
  await folder.waitFor();
  if (await folder.getAttribute("aria-expanded") !== "true") await folder.click();
  await page.locator(".project-tree__topic-window-toggle").first().click();
  await page.getByText("cache-0", { exact: true }).waitFor();
  for (let round = 0; round < 4; round++) {
    for (const fixture of round % 2 ? [...fixtures].reverse() : fixtures) {
      await select(fixture);
      await pageHistory(fixture);
    }
    console.log(`PASS native cache round ${round + 1}: eight sessions, older/latest paging and settled budgets`);
  }
  for (const fixture of [fixtures[0], fixtures[1], fixtures[0]]) { await select(fixture); await sample(fixture.id, "A-B-A"); }
  assert.ok(report.samples.some(sample => sample.cache.historyEvictions > 0), "no resident-session eviction exercised");
  assert.ok(report.samples.some(sample => sample.cache.reclaimedPages > 0), "no page reclamation exercised");
  assert.ok(report.samples.some(sample => sample.cache.markdownBytes > 0), "no parsed Markdown cached");
  assert.ok(report.samples.some(sample => sample.objectURLs?.created > 0), "no native SVG preview URL created");
  assert.ok(report.samples.some(sample => sample.objectURLs?.revoked > 0), "no native SVG preview URL released");
  for (const fixture of fixtures) for (const file of fixture.files) assert.equal(hash(file.path), file.digest, "authoritative source changed");
  const ready = readFileSync(join(home, "desktop-shell/logs/shell.log"), "utf8").split("\n").reverse().map(parseServiceReady).find(Boolean);
  assert.ok(ready);
  await closeAndVerify(application, { shellPid: await application.evaluate(() => process.pid), servicePid: ready.pid });
  application = undefined;
  report.complete = true;
} catch (error) {
  report.error = String(error);
  if (page) writeFileSync(join(home, "failure.json"), JSON.stringify(await page.evaluate(() => ({ text: document.querySelector(".chat-column")?.innerText, stats: window.__reasonixPerf?.stats(), objectURLs: window.__historyObjectURLs?.() })).catch(error => ({ error: String(error) })), null, 2));
  await page?.screenshot({ path: join(home, "failure.png"), fullPage: true }).catch(() => {});
  throw error;
} finally {
  writeFileSync(resultPath, JSON.stringify(report, null, 2));
  await application?.close();
  if (lease && lease.exitCode === null) { const exited = once(lease, "exit"); lease.stdin.end(); await exited; }
  if (report.complete) rmSync(home, { recursive: true, force: true });
  else console.error(`Native cache fixture retained at ${home}`);
  console.log(`Native cache results: ${resultPath}`);
}
