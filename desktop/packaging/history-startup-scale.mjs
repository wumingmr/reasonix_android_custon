// Real-package startup measurements with a fixed active session and project.
// Usage: node desktop/packaging/history-startup-scale.mjs /path/Reasonix.app [--runs=30] [--sizes=100,10000,100000]
// Measures warm catalog startup; this does not certify all formats, platforms,
// body-read counts, memory budgets, or the absence of every background scan.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { createServer } from "node:http";
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir, platform, arch, cpus, totalmem } from "node:os";
import { packagedSmokeEnv } from "./smoke-env.mjs";
import { parseServiceReady, waitForSmokeCondition } from "./smoke-poll.mjs";
import { closeAndVerify } from "./smoke-lifecycle.mjs";

assert.equal(platform(), "darwin", "This harness measures the macOS production package");
const bundle = resolve(process.argv[2]);
const args = new Map(process.argv.slice(3).map(value => value.replace(/^--/, "").split("=")));
const runs = Number(args.get("runs") ?? 30);
const sizes = (args.get("sizes") ?? "100,10000,100000").split(",").map(Number);
assert.ok(Number.isSafeInteger(runs) && runs > 0 && runs <= 100);
assert.ok(sizes.length > 0 && sizes.every((n, i) => Number.isSafeInteger(n) && n >= 50 && n <= 100000 && (i === 0 || n > sizes[i - 1])));
const require = createRequire(new URL("../electron/package.json", import.meta.url));
const { _electron } = require("playwright");
const home = await mkdtemp(join(tmpdir(), "reasonix-history-scale-"));
const resultPath = `${home}.results.json`;
const records = [];
const warmups = [];
const build = JSON.parse(await readFile(join(bundle, "Contents/Resources/build.json"), "utf8"));
let application, expectedSessionID, seeded = 0, providerCalls = 0;
const server = createServer(async (req, res) => {
  for await (const _ of req) { /* drain the local fixture request */ }
  providerCalls++;
  res.writeHead(200, { "Content-Type": "text/event-stream" });
  res.end(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { content: "ACTIVE_SCALE_ANSWER" }, finish_reason: null }] })}\n\ndata: ${JSON.stringify({ choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\ndata: [DONE]\n\n`);
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
await writeFile(join(home, "config.toml"), `default_model = "fixture/model"\n[desktop]\nprovider_access = ["fixture"]\n[[providers]]\nname = "fixture"\nkind = "openai"\nbase_url = "http://127.0.0.1:${server.address().port}/v1"\nmodels = ["model"]\ndefault = "model"\napi_key_env = "HISTORY_SCALE_FIXTURE_KEY"\n`);
const environment = { ...packagedSmokeEnv(process.env, home), HISTORY_SCALE_FIXTURE_KEY: "local-fixture" };
const invoke = (page, method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
const now = () => performance.now();
async function launch() {
  const started = now();
  application = await _electron.launch({ executablePath: join(bundle, "Contents/MacOS/Reasonix"), env: environment });
  const page = await application.firstWindow();
  page.setDefaultTimeout(30000);
  await page.waitForFunction(() => Boolean(window.reasonixDesktop));
  await invoke(page, "Version");
  return { page, started };
}
async function close() {
  if (!application) return;
  // Logs span restarts. Obtain the service of this shell, not the first
  // historical ready line in the append-only log.
  const log = await readFile(join(home, "desktop-shell/logs/shell.log"), "utf8");
  const latest = log.split("\n").reverse().map(parseServiceReady).find(Boolean);
  assert.ok(latest, "service handshake missing");
  await closeAndVerify(application, { shellPid: await application.evaluate(() => process.pid), servicePid: latest.pid });
  application = undefined;
}
async function settleCatalog(page, count) {
  const started = now();
  let nextProgress = 0;
  await waitForSmokeCondition(async () => {
    const snapshot = await invoke(page, "GetProjectTreeSnapshot");
    if (now() >= nextProgress) {
      nextProgress = now() + 10000;
      console.log(JSON.stringify({ phase: "warm_progress", sessions: count, elapsedMs: now() - started,
        indexed: snapshot.catalog.indexed, total: snapshot.catalog.total, state: snapshot.catalog.state }));
    }
    if (snapshot.catalog.indexed < count) return false;
    const first = await invoke(page, "ListProjectTopics", [{ scope: "global", limit: 50 }]);
    return first.complete === true && first.items.length === 50;
  }, { timeout: 300000, interval: 1000 });
  warmups.push({ sessions: count, elapsedMs: now() - started });
}
function summary() {
  const p95 = values => [...values].sort((a, b) => a - b)[Math.ceil(values.length * .95) - 1];
  const cohorts = sizes.map(size => {
    const rows = records.filter(row => row.sessions === size);
    return { sessions: size, runs: rows.length, p95: Object.fromEntries(["interactiveMs", "firstPageMs", "trustedWindowMs", "canSendMs"].map(key => [key, rows.length ? p95(rows.map(row => row[key])) : null])) };
  });
  const complete = cohorts.every(cohort => cohort.runs === runs);
  const qualifiedSample = complete && runs >= 30 && sizes.join(",") === "100,10000,100000";
  const thresholdPass = qualifiedSample && ["interactiveMs", "firstPageMs"].every(key => cohorts.at(-1).p95[key] <= cohorts[0].p95[key] + Math.max(200, cohorts[0].p95[key] * .2));
  return { build, machine: { platform: platform(), arch: arch(), cpu: cpus()[0]?.model, logicalCPUs: cpus().length, memoryBytes: totalmem() },
    measurement: "elapsed from package launch; existing catalog; fixed active session/project; progressively added inactive legacy files",
    qualifiedSample, thresholdPass, warmups, cohorts, records };
}
try {
  const { page } = await launch();
  await invoke(page, "CreateSession", ["global"]);
  await page.locator("#composer-input").fill("ACTIVE_SCALE_PROMPT");
  await page.locator(".composer__btn--send").click();
  await page.waitForFunction(() => document.querySelector(".chat-transcript")?.textContent?.includes("ACTIVE_SCALE_ANSWER"));
  await waitForSmokeCondition(async () => (await invoke(page, "ListTabs")).every(tab => !tab.running));
  expectedSessionID = (await invoke(page, "ListTabs")).find(tab => tab.active)?.session?.sessionId;
  assert.ok(expectedSessionID);
  await close();
  await mkdir(join(home, "sessions"), { recursive: true });
  for (const size of sizes) {
    for (; seeded < size;) {
      const end = Math.min(size, seeded + 64);
      await Promise.all(Array.from({ length: end - seeded }, (_, offset) => writeFile(join(home, "sessions", `inactive-${String(seeded + offset).padStart(6, "0")}.jsonl`), '{"role":"user","content":"INACTIVE_SCALE_BODY"}\n')));
      seeded = end;
    }
    console.log(JSON.stringify({ phase: "warm_catalog", sessions: size }));
    const warm = await launch();
    await settleCatalog(warm.page, size);
    await close();
    for (let run = 1; run <= runs; run++) {
      const { page, started } = await launch();
      const elapsed = () => now() - started;
      const interactive = page.locator(".sidebar__quick-action").waitFor({ state: "visible" }).then(elapsed);
      const firstPage = waitForSmokeCondition(async () => {
        const value = await invoke(page, "ListProjectTopics", [{ scope: "global", limit: 50 }]);
        assert.ok(value.items.length <= 50, "first-page limit exceeded");
        return value.items.length === 50;
      }).then(elapsed);
      const trustedWindow = page.waitForFunction(() => document.querySelector(".chat-transcript")?.textContent?.includes("ACTIVE_SCALE_ANSWER")).then(elapsed);
      const canSend = (async () => {
        await page.locator("#composer-input").fill("UNSENT_SCALE_DRAFT");
        await waitForSmokeCondition(() => page.locator(".composer__btn--send").isEnabled());
        return elapsed();
      })();
      const [interactiveMs, firstPageMs, trustedWindowMs, canSendMs] = await Promise.all([interactive, firstPage, trustedWindow, canSend]);
      assert.equal((await invoke(page, "ListTabs")).find(tab => tab.active)?.session?.sessionId, expectedSessionID);
      assert.equal(providerCalls, 1, "startup or draft editing unexpectedly executed a model turn");
      await close();
      const record = { sessions: size, run, interactiveMs, firstPageMs, trustedWindowMs, canSendMs };
      records.push(record);
      await writeFile(resultPath, JSON.stringify(summary(), null, 2));
      console.log(JSON.stringify(record));
    }
  }
  console.log(JSON.stringify({ phase: "complete", resultPath, ...summary() }));
  if (summary().qualifiedSample && !summary().thresholdPass) process.exitCode = 1;
  await rm(home, { recursive: true, force: true });
} catch (error) {
  await writeFile(resultPath, JSON.stringify({ ...summary(), error: String(error), retainedFixture: home }, null, 2));
  console.error(`Scale fixture retained at ${home}; results at ${resultPath}`);
  throw error;
} finally {
  await close();
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
}
