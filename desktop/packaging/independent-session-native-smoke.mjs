// Real Electron shell + full App + current Go service, with a disposable home.
// Build first: cd desktop && go build -o build/bin/reasonix-desktop-service .
// Then: cd electron && pnpm build
// Also build the renderer: cd desktop/frontend && pnpm build
// Run: node desktop/packaging/independent-session-native-smoke.mjs [service] [evidence]
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { createServer as createHTTPServer } from "node:http";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from "node:fs";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import { packagedSmokeEnv } from "./smoke-env.mjs";
import { waitForSmokeCondition } from "./smoke-poll.mjs";

const desktop = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const requireShell = createRequire(join(desktop, "electron/package.json"));
const requireFrontend = createRequire(join(desktop, "frontend/package.json"));
const { _electron } = requireShell("playwright");
const { preview } = await import(requireFrontend.resolve("vite"));
const service = resolve(process.argv[2] || join(desktop, "build/bin/reasonix-desktop-service"));
const evidence = resolve(process.argv[3] || join(tmpdir(), "reasonix-independent-native"));
const home = mkdtempSync(join(tmpdir(), "reasonix-independent-native-home-"));
mkdirSync(evidence, { recursive: true });
const checks = [];
const errors = [];
const violations = [];
let releaseBackground;
const record = check => { checks.push(check); console.log(`PASS ${check}`); };
const provider = createHTTPServer(async (req, res) => {
  let raw = "";
  for await (const chunk of req) raw += chunk;
  const messages = JSON.stringify(JSON.parse(raw || "{}").messages || []);
  const content = messages.includes("BACKGROUND_CHILD") ? "ANSWER_BACKGROUND_CHILD"
    : messages.includes("CHILD_ONLY") ? "ANSWER_CHILD_ONLY" : "ANSWER_PARENT_RETAINED";
  if (messages.includes("BACKGROUND_CHILD")) await new Promise(resolve => { releaseBackground = resolve; });
  res.writeHead(200, { "Content-Type": "text/event-stream" });
  res.end(`data: ${JSON.stringify({ id: "independent-fixture", choices: [{ index: 0, delta: { content }, finish_reason: null }] })}\n\n`
    + `data: ${JSON.stringify({ id: "independent-fixture", choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\ndata: [DONE]\n\n`);
});
await new Promise(resolve => provider.listen(0, "127.0.0.1", resolve));
writeFileSync(join(home, "config.toml"), `default_model = "fixture/model"\n[desktop]\nprovider_access = ["fixture"]\n[[providers]]\nname = "fixture"\nkind = "openai"\nbase_url = "http://127.0.0.1:${provider.address().port}/v1"\nmodels = ["model"]\ndefault = "model"\napi_key_env = "INDEPENDENT_FIXTURE_KEY"\n`);
// Qualify the finished renderer build. A cold dev server transforms thousands
// of modules during shell startup and is not the shipped renderer artifact.
assert.ok(existsSync(join(desktop, "frontend/dist/index.html")), "build the renderer before native qualification");
const vite = await preview({ root: join(desktop, "frontend"), logLevel: "error", preview: { host: "127.0.0.1", port: 0 } });
let application, page;
const invoke = (method, args = []) => page.evaluate(({ method, args }) => window.reasonixDesktop.invoke(method, args), { method, args });
const active = async () => {
  const tabs = await invoke("ListTabs");
  return tabs.find(tab => tab.active) ?? (tabs.length === 1 ? tabs[0] : undefined);
};
async function capturePhase(phase) {
  const snapshot = { tabs: await invoke("ListTabs"), workspace: await invoke("GetWorkspaceSnapshot") };
  writeFileSync(join(evidence, `phase-${phase}.json`), JSON.stringify(snapshot, null, 2));
  return snapshot;
}
async function list() {
  // Catalog hydration can invalidate a snapshot during startup; the contract
  // requires a fresh first-page read, never appending to the rejected page.
  for (let attempt = 0; ; attempt++) {
    try { return await invoke("ListProjectTopics", [{ scope: "global", workspaceRoot: "", limit: 50 }]); }
    catch (error) { if (attempt >= 2 || !String(error).includes("stale_cursor")) throw error; }
  }
}
const row = title => page.locator(".project-tree__topic-main").filter({ has: page.getByText(title, { exact: true }) });
const settle = () => waitForSmokeCondition(async () => (await invoke("ListTabs")).every(tab => !tab.running));
async function launch() {
  application = await _electron.launch({ args: [join(desktop, "electron")], env: {
    ...packagedSmokeEnv(process.env, home), REASONIX_DEV: "1", REASONIX_DESKTOP_SERVICE: service,
    REASONIX_ELECTRON_DEV_URL: `http://127.0.0.1:${vite.httpServer.address().port}`,
    INDEPENDENT_FIXTURE_KEY: "loopback-only",
  }, timeout: 60_000 });
  page = await application.firstWindow();
  page.setDefaultTimeout(30000);
  page.on("pageerror", error => errors.push(error.message));
  await page.waitForFunction(() => Boolean(window.reasonixDesktop));
  await page.evaluate(() => new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error("native service readiness timed out")), 30000);
    const off = window.reasonixDesktop.native.onServiceState(state => {
      if (state.phase === "ready") queueMicrotask(() => { clearTimeout(timeout); off(); resolve(); });
      else if (["failed", "exited"].includes(state.phase)) queueMicrotask(() => { clearTimeout(timeout); off(); reject(new Error(JSON.stringify(state))); });
    });
  }));
  // Absence of the boot placeholder is also true before React mounts. Wait
  // for the actual interactive surface before issuing a user navigation;
  // otherwise the fixture races initial tab restoration with EnsureBlank.
  await page.locator("textarea").first().waitFor({ state: "visible" });
}
async function close() { await application.close(); application = null; }
async function send(text, response) {
  const composer = page.locator("textarea").first();
  await composer.fill(text);
  await page.locator(".composer__btn--send").click();
  await page.waitForFunction(text => document.querySelector(".chat-transcript")?.textContent?.includes(text), response);
  await waitForSmokeCondition(async () => Boolean((await active())?.session?.sessionId));
  await settle();
}
async function select(title, ref) {
  await row(title).click();
  await waitForSmokeCondition(async () => (await active())?.session?.sessionId === ref.sessionId);
  await page.waitForFunction(() => document.querySelector(".transcript-navigation-surface")?.getAttribute("aria-busy") === "false"
    && Boolean(document.querySelector(".chat-transcript")?.textContent?.includes("ANSWER_PARENT_RETAINED")));
  await page.waitForTimeout(500); // Observe post-hydration title rather than the optimistically selected shell.
  const visibleTitle = await page.locator(".topicbar h1").innerText();
  if (visibleTitle !== title) violations.push({ check: "active title follows saved session presentation", expected: title, actual: visibleTitle });
  const activeRows = await page.locator(".project-tree__topic--active").count();
  if (activeRows !== 1) violations.push({ check: "exactly one selected sidebar row", expected: 1, actual: activeRows, sessionId: ref.sessionId });
  assert.equal(await page.locator(".project-tree__topic-main").count(), 2, "opening one session must not add a third runtime projection");
}
try {
  await launch();
  const initialBlank = await capturePhase("initial");
  const version = await invoke("Version");
  await page.locator(".sidebar__quick-action").click();
  await page.locator(".session-draft-surface").waitFor({ state: "visible" });
  const createdBlank = await capturePhase("created-blank");
  assert.equal(initialBlank.tabs.length, 0, "fresh home starts without a durable session");
  assert.equal(createdBlank.tabs.length, 0, "blank draft does not create a canonical session before first send");
  assert.equal(createdBlank.workspace.pendingCreates.length, 0, "blank draft leaves no abandoned pending create");
  assert.equal(createdBlank.workspace.workspaces.flatMap(workspace => workspace.sessionIds).length, 0,
    "blank draft leaves no abandoned recovery row");
  record("blank startup stays a durable draft without abandoned canonical or pending rows");
  await send("PARENT_RETAINED", "ANSWER_PARENT_RETAINED");
  await capturePhase("sent");
  const parent = (await active()).session;
  await invoke("RenameSessionTarget", [{ ref: parent }, "Native parent A"]);
  const targets = await invoke("ForkTargetsForTab", [(await active()).id]);
  const boundary = targets.targets.find(target => target.available);
  assert.ok(boundary, "completed real provider turn exposes a verifiable fork boundary");
  const child = await invoke("ForkSessionTarget", [{ ref: parent }, boundary.turnId]);
  assert.notEqual(parent.sessionId, child.sessionId);
  await invoke("RenameSessionTarget", [{ ref: child }, "Native child B"]);
  const forked = await list();
  const sourceTopic = forked.items.find(node => node.session?.sessionId === parent.sessionId).topicId;
  await capturePhase("forked");
  // Seed upgrade-era shared-topic presentation only after the isolated owner stops.
  // Current forks may receive distinct topic metadata; transcript and SessionIDs
  // remain untouched so the reopened real service exercises existing A/B history.
  await close();
  const registryPath = join(home, "desktop/workspace-state-v1.json");
  const registry = JSON.parse(readFileSync(registryPath, "utf8"));
  registry.presentation[child.sessionId].topicId = sourceTopic;
  writeFileSync(registryPath, JSON.stringify(registry));
  await launch();
  const initial = await list();
  const a = initial.items.find(node => node.session?.sessionId === parent.sessionId);
  const b = initial.items.find(node => node.session?.sessionId === child.sessionId);
  assert.ok(a && b);
  assert.equal(a.topicId, b.topicId, "fixture must exercise the same TopicID");
  await row("Native parent A").waitFor();
  await row("Native child B").waitFor();
  assert.equal(await page.locator(".project-tree__topic-main").count(), 2, "directory and runtime coalesce into the same two logical rows");
  assert.equal((await row("Native parent A").boundingBox()).x, (await row("Native child B").boundingBox()).x);
  record("real fork with shared TopicID is displayed as two sibling rows");
  await select("Native child B", child);
  await page.locator(".topicbar__title-button").click();
  await page.locator(".topicbar__title-input").fill("Native child renamed B");
  await page.locator(".topicbar__title-input").press("Enter");
  await row("Native child renamed B").waitFor();
  assert.equal((await list()).items.find(node => node.session?.sessionId === parent.sessionId).label, "Native parent A");
  await send("CHILD_ONLY", "ANSWER_CHILD_ONLY");
  assert.ok(!JSON.stringify(await invoke("ReadSessionHistory", [parent, "", 32])).includes("CHILD_ONLY"));
  record("top rename and further child turn leave parent title/history intact");
  await page.locator("textarea").first().fill("BACKGROUND_CHILD");
  await page.locator(".composer__btn--send").click();
  await waitForSmokeCondition(async () => Boolean(releaseBackground) && Boolean((await active())?.running));
  await select("Native parent A", parent);
  releaseBackground();
  await waitForSmokeCondition(async () => JSON.stringify(await invoke("ReadSessionHistory", [child, "", 32])).includes("ANSWER_BACKGROUND_CHILD"));
  assert.equal((await active()).session.sessionId, parent.sessionId);
  assert.ok(!JSON.stringify(await invoke("ReadSessionHistory", [parent, "", 32])).includes("BACKGROUND_CHILD"));
  record("switching away lets B complete in background without changing A runtime or history");
  for (const [title, ref] of [["Native parent A", parent], ["Native child renamed B", child], ["Native parent A", parent]]) await select(title, ref);
  record("A/B/A sidebar navigation resolves the exact native runtime");
  await row("Native child renamed B").click({ button: "right" });
  await page.getByRole("menuitem", { name: /Move to trash|移至回收站|移到回收站|移至垃圾桶/ }).click();
  await page.getByRole("menuitem", { name: /Confirm|确认|確認/ }).click();
  await waitForSmokeCondition(async () => (await invoke("GetWorkspaceSnapshot")).archivedSessionIds.includes(child.sessionId));
  await row("Native child renamed B").waitFor({ state: "hidden" });
  await row("Native parent A").waitFor();
  await page.screenshot({ path: join(evidence, "archived-child.png") });
  await close();
  await launch();
  assert.ok((await invoke("GetWorkspaceSnapshot")).archivedSessionIds.includes(child.sessionId));
  assert.ok(!(await list()).items.some(node => node.session?.sessionId === child.sessionId));
  assert.ok((await list()).items.some(node => node.session?.sessionId === parent.sessionId));
  record("native menu archives only B and restart preserves the lifecycle");
  await invoke("RestoreSessionTarget", [{ ref: child }]);
  await row("Native child renamed B").waitFor();
  await select("Native child renamed B", child);
  await page.waitForFunction(() => document.querySelector(".chat-transcript")?.textContent?.includes("ANSWER_CHILD_ONLY"));
  await close();
  await launch();
  await select("Native child renamed B", child);
  await page.waitForFunction(() => document.querySelector(".chat-transcript")?.textContent?.includes("ANSWER_CHILD_ONLY"));
  record("restore retains child SessionID and transcript across another native restart");
  assert.deepEqual(errors, []);
  assert.deepEqual(violations, [], "all observed native invariants must hold");
  await page.screenshot({ path: join(evidence, "restored-child.png") });
  writeFileSync(join(evidence, "result.json"), JSON.stringify({ passed: true, version, parent, child, checks, errors,
    scope: "real Electron development shell, built production App renderer, real Go service, loopback provider" }, null, 2));
} catch (error) {
  writeFileSync(join(evidence, "result.json"), JSON.stringify({ passed: false, checks, errors, violations, error: String(error.stack) }, null, 2));
  if (page && !page.isClosed()) {
    await page.screenshot({ path: join(evidence, "failure.png") }).catch(() => {});
    writeFileSync(join(evidence, "failure-state.json"), JSON.stringify({
      body: await page.locator("body").innerText().catch(() => ""), tabs: await invoke("ListTabs").catch(() => null),
      topics: await list().catch(() => null), workspace: await invoke("GetWorkspaceSnapshot").catch(() => null),
    }, null, 2));
  }
  throw error;
} finally {
  releaseBackground?.();
  if (application) await close();
  for (const name of ["shell.log", "service.log"]) {
    const source = join(home, "desktop-shell/logs", name);
    if (existsSync(source)) writeFileSync(join(evidence, name), readFileSync(source));
  }
  await vite.close();
  provider.closeAllConnections();
  await new Promise(resolve => provider.close(resolve));
  rmSync(home, { recursive: true, force: true });
}
