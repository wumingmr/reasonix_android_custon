// Actual packaged shell/service, disposable home, loopback provider only.
// Usage: node desktop/packaging/provider-diagnostic-smoke.mjs /path/Reasonix.app
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { packagedSmokeEnv } from "./smoke-env.mjs";
import { waitForSmokeCondition, parseServiceReady } from "./smoke-poll.mjs";
import { closeAndVerify, processAlive, waitForProcessesToExit } from "./smoke-lifecycle.mjs";

const require = createRequire(new URL("../electron/package.json", import.meta.url));
const { _electron } = require("playwright");
const bundle = resolve(process.argv[2]);
const home = mkdtempSync(join(tmpdir(), "reasonix-provider-native-"));
const output = process.env.REASONIX_SMOKE_ARTIFACTS || home;
mkdirSync(output, { recursive: true });
let mode = "hold", calls = 0;
const sockets = new Set();
const server = createServer((req) => {
  calls++;
  // Drain first and close with FIN so the live fixture consistently produces
  // EOF rather than a platform-dependent reset while the body is still writing.
  if (mode === "eof") req.on("end", () => req.socket.end());
  req.resume();
});
server.on("connection", socket => { sockets.add(socket); socket.on("close", () => sockets.delete(socket)); });
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
mkdirSync(join(home, "sessions"));
writeFileSync(join(home, "config.toml"), `default_model = "fixture/model"\n[desktop]\nprovider_access = ["fixture"]\n[[providers]]\nname = "fixture"\nkind = "openai"\nbase_url = "http://127.0.0.1:${server.address().port}/v1"\nmodels = ["model"]\ndefault = "model"\napi_key_env = "PROVIDER_FIXTURE_KEY"\n`);
const fixturePath = join(home, "sessions", "send-failure.jsonl");
writeFileSync(fixturePath, [
  { role: "user", content: "Historical send failure" },
  { role: "tool", local_only: true, interrupted_turn: { pending: true, terminalStatus: "failed", failureDiagnostic: { kind: "request_send_timeout", providerId: "fixture", protocol: "openai" } } },
].map(v => JSON.stringify(v)).join("\n") + "\n");
let application, page, passed = false;
const quitFailures = [];
const invoke = (method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
async function launch() {
  const build = JSON.parse(readFileSync(join(bundle, "Contents/Resources/build.json"), "utf8"));
  application = await _electron.launch({ executablePath: join(bundle, "Contents/MacOS/Reasonix"),
    env: { ...packagedSmokeEnv(process.env, home), PROVIDER_FIXTURE_KEY: "local-fixture-key" }, timeout: 60000 });
  await waitForSmokeCondition(async () => {
    for (const p of application.windows()) {
      const version = await p.evaluate(() => window.reasonixDesktop?.invoke("Version", [])).catch(() => null);
      if (version === build.version) { page = p; return true; }
    }
    return false;
  }, { timeout: 60000 });
  assert.equal(await invoke("Version"), build.version);
  assert.equal(await application.evaluate(({ app }) => app.isPackaged && !process.env.REASONIX_DEV), true);
}
async function quit() {
  const ready = readFileSync(join(home, "desktop-shell/logs/shell.log"), "utf8").split("\n").reverse().map(parseServiceReady).find(Boolean);
  assert.ok(ready, "packaged service announced readiness");
  const shellPid = await application.evaluate(() => process.pid);
  try {
    await closeAndVerify(application, { shellPid, servicePid: ready.pid });
  } catch (error) {
    console.error(`Quit verification failed: shellAlive=${processAlive(shellPid)} serviceAlive=${processAlive(ready.pid)}`);
    quitFailures.push(error);
    // Retain the failed verdict, but still exercise cold recovery/export after
    // cleanup. Only these two processes were created in this disposable home.
    for (const pid of [shellPid, ready.pid]) if (processAlive(pid)) process.kill(pid, "SIGKILL");
    await waitForProcessesToExit([shellPid, ready.pid]);
  }
  application = null;
}
async function activeTab() { return (await invoke("ListTabs")).find(t => t.active); }
async function exportDiagnostic(name) {
  const path = join(output, name);
  // Choose a deterministic destination through the real native-dialog owner.
  await application.evaluate(({ dialog }, path) => { dialog.showSaveDialog = async () => ({ canceled: false, filePath: path }); }, path);
  const tab = await activeTab();
  const handle = await invoke("BeginSessionExportForTarget", [{ sessionPath: tab.sessionPath }, tab.id, "diagnostic", "provider-smoke", ""]);
  assert.ok(handle.exportId);
  await invoke("FinishSessionExport", [handle.exportId]);
  const raw = readFileSync(path, "utf8");
  assert.doesNotThrow(() => JSON.parse(raw));
  assert.ok(!raw.includes("local-fixture-key"));
  return raw;
}
try {
  await launch();
  const folder = page.locator(".project-tree__folder-main").first();
  await folder.waitFor();
  if (await folder.getAttribute("aria-expanded") !== "true") await folder.click();
  await page.locator(".project-tree__topic-main").filter({ has: page.getByText("send-failure", { exact: true }) }).click();
  await page.waitForFunction(() => document.querySelector(".chat-transcript")?.textContent?.includes("300"));
  await page.locator(".user-error__toggle").first().click();
  await page.screenshot({ path: join(output, "provider-send-timeout.png") });
  await waitForSmokeCondition(async () => (await activeTab())?.ready === true);
  // Legacy fixtures are read-only sources until the normal import flow prepares
  // a canonical session. Use the same RPCs as the historical-source banner.
  const historical = (await activeTab()).historicalSource;
  if (historical) {
    let preparation = await invoke("PrepareSession", [{ source: historical }]);
    await waitForSmokeCondition(async () => {
      preparation = await invoke("GetSessionPreparation", [preparation.operationId]);
      assert.ok(!["failed", "blocked"].includes(preparation.status), JSON.stringify(preparation));
      return preparation.status === "ready";
    });
    await invoke("OpenSession", [preparation.target]);
    await waitForSmokeCondition(async () => {
      const current = await activeTab();
      return current?.ready && !current.historicalSource;
    });
  }
  const tab = await activeTab();
  await invoke("SubmitToTabWithID", [tab.id, "Cancellation fixture", "cancel-fixture"]);
  await waitForSmokeCondition(() => calls > 0);
  await invoke("CancelTab", [tab.id]);
  await waitForSmokeCondition(async () => !(await activeTab()).running);
  console.log("PASS packaged stop operation against a waiting loopback provider");
  const before = calls;
  mode = "eof";
  await invoke("SubmitToTabWithID", [tab.id, "EOF fixture", "eof-fixture"]);
  await waitForSmokeCondition(() => calls > before);
  await page.waitForFunction(() => [...document.querySelectorAll(".user-error")].some(e => e.textContent?.includes("EOF")));
  await waitForSmokeCondition(async () => !(await activeTab()).running);
  const live = await exportDiagnostic("provider-live.json");
  assert.match(live, /transport_eof/);
  assert.match(live, /errorCode/);
  await page.screenshot({ path: join(output, "provider-eof-live.png") });
  await quit();
  await launch();
  await page.waitForFunction(() => [...document.querySelectorAll(".user-error")].some(e => e.textContent?.includes("EOF")));
  const cold = await exportDiagnostic("provider-cold.json");
  assert.match(cold, /transport_eof/);
  assert.match(cold, /diagnostic\/provider/);
  assert.match(cold, /errorCode/);
  await page.screenshot({ path: join(output, "provider-eof-reopened.png") });
  console.log("PASS packaged historical timeout details, live EOF, restart and safe cold export");
  await quit();
  assert.equal(quitFailures.length, 0, "normal quit failed; forced cleanup is not a passing lifecycle check");
  passed = true;
  console.log("PASS packaged clean exit");
} finally {
  if (application) await quit().catch(() => application.close());
  for (const socket of sockets) socket.destroy();
  await new Promise(resolve => server.close(resolve));
  if (passed && output !== home) rmSync(home, { recursive: true, force: true });
  console.log(`Native smoke artifacts: ${output}`);
  if (!passed) console.log(`Failed smoke home: ${home}`);
}
