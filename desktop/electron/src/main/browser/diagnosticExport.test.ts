import assert from "node:assert/strict";
import { test } from "node:test";
import { BrowserDiagnosticExport } from "./diagnosticExport.js";
import { DiagnosticBuffer } from "./diagnostics.js";
import { FakeViewFactory, silentLog } from "./fakeGuestViews.js";
import { GrantRegistry } from "./grants.js";
import { BrowserSurfaceManager } from "./surfaceManager.js";
import { buildBrowserHostCalls } from "./hostCalls.js";
import { ActionExecutor } from "./actions.js";
import { DocumentRegistry } from "./documents.js";
import { DownloadTracker } from "./downloads.js";
import { acquireDebugger, disposeDebugger } from "./debuggerLease.js";

const a = "a".repeat(64), b = "b".repeat(64);
function setup(snapshot?: (tab: import("./surfaceManager.js").BrowserTab) => Promise<never>) {
  const factory = new FakeViewFactory();
  const surfaces = new BrowserSurfaceManager({ views: factory, contentSize: () => null, onTakeover() {}, onCrash() {}, log: silentLog });
  const grants = new GrantRegistry({ generation: () => "generation" });
  const diagnostics = new BrowserDiagnosticExport(surfaces, grants, { build: "test-build", version: "test", platform: "test" });
  const documents = new DocumentRegistry();
  const calls = buildBrowserHostCalls({ surfaces, grants, diagnosticExport: diagnostics, documents, actions: new ActionExecutor({ surfaces, documents }), downloads: new DownloadTracker({ tabForWebContents: () => undefined, defaultDirectory: () => "/tmp", onUpdate() {}, log: silentLog }), snapshot });
  return { factory, surfaces, grants, diagnostics, calls };
}

test("export retains scoped closed-tab events, sanitizes pages and releases listeners", async () => {
  const s = setup();
  await s.calls["host/browser.grant"]({ grantId: "grant-a", tabId: "task-a", sessionId: "session", diagnosticScope: a });
  await s.calls["host/browser.grant"]({ grantId: "grant-b", tabId: "task-b", sessionId: "session", diagnosticScope: b });
  const tab = await s.surfaces.open("https://example.test/?token=SECRET", { taskId: "task-a", sessionId: "session", temporary: true });
  const other = await s.surfaces.open("https://other.test", { taskId: "task-b", sessionId: "session", temporary: false });
  const page = tab.view.diagnostics = new DiagnosticBuffer();
  const otherPage = other.view.diagnostics = new DiagnosticBuffer();
  s.diagnostics.read(a); // Attach newly materialized page buffers.
  page.add({ kind: "console", message: "token=SECRET cookie: SECRET", url: "https://user:SECRET@example.test/path?token=SECRET#SECRET" });
  otherPage.add({ kind: "exception", message: "FOREIGN-PAGE" });
  s.surfaces.takeover(tab.id, "user");
  s.grants.revoke("grant-a");
  s.surfaces.close(tab.id);
  page.add({ kind: "console", message: "LATE-CLOSED-PAGE" });
  const result = s.diagnostics.read(a);
  const data = JSON.stringify(result);
  for (const text of ["SECRET", "grant-a", "FOREIGN-PAGE", "LATE-CLOSED-PAGE"]) assert.equal(data.includes(text), false, text);
  assert.ok(result.entries.some(row => row.event === "tab_closed"));
  assert.ok(result.entries.some(row => row.event === "grant_revoked"));
  assert.ok(result.entries.some(row => row.mode === "human"));
  assert.ok(result.entries.some(row => row.kind === "page"));
  assert.equal(result.tabs.length, 0);
  s.diagnostics.dispose();
  const restarted = new BrowserDiagnosticExport(s.surfaces, s.grants, { build: "new-build", version: "test", platform: "test" });
  assert.equal(restarted.read(a).entries.length, 0, "unattributed history must not be guessed after restart");
  restarted.dispose();
  s.surfaces.destroyAll();
});

test("queued requests retain CDP request and operation identity without arguments", async () => {
  const releases: (() => void)[] = [];
  let enteredSecond!: () => void;
  const secondStarted = new Promise<void>(resolve => { enteredSecond = resolve; });
  const s = setup(async tab => {
    const release = acquireDebugger(tab.view.page.debugger);
    try { await release.send("Page.createIsolatedWorld", { expression: "PRIVATE-PAGE" }, "private-session"); throw new Error("script failure token=SECRET"); }
    finally { release(); disposeDebugger(tab.view.page.debugger); }
  });
  for (const [scope, task] of [[a, "a"], [b, "b"]]) {
    await s.calls["host/browser.grant"]({ grantId: task, tabId: task, sessionId: "session", diagnosticScope: scope });
    const tab = await s.surfaces.open("https://example.test", { taskId: task, sessionId: "session", temporary: false });
    s.factory.views.at(-1)!.page.debugger.respond = () => new Promise(resolve => { releases.push(() => resolve({})); if (releases.length === 2) enteredSecond(); });
    // Start both requests before delivering either CDP response.
    const pending = s.calls["host/browser.snapshot"]({ grantId: task, tabId: tab.id, requestId: `request-${task}`, operationId: `operation-${task}` });
    (tab as typeof tab & { pending: Promise<unknown> }).pending = Promise.resolve(pending).then(() => assert.fail("expected failure"), () => {});
  }
  assert.equal(releases.length, 1, "shared capture surface must serialize requests");
  releases[0]();
  await secondStarted;
  releases[1]();
  await Promise.all(s.surfaces.all().map(tab => (tab as typeof tab & { pending: Promise<unknown> }).pending));
  for (const [scope, task] of [[a, "a"], [b, "b"]]) {
    const result = s.diagnostics.read(scope);
    const commands = result.entries.filter(row => row.command);
    assert.deepEqual(commands.map(row => row.phase), ["cdp_started", "cdp_completed"]);
    assert.ok(commands.every(row => row.requestId === `request-${task}` && row.operationId === `operation-${task}` && row.target === "child"));
    assert.ok(result.entries.some(row => row.phase === "failed" && row.requestId === `request-${task}`));
    assert.equal(JSON.stringify(result).includes("PRIVATE-PAGE"), false);
    assert.equal(JSON.stringify(result).includes("SECRET"), false);
  }
  s.surfaces.destroyAll(); s.diagnostics.dispose();
});

test("export has honest global eviction and no page credentials in bounded payload", () => {
  const s = setup();
  for (let i = 0; i < 700; i++) s.diagnostics.trace(a, { requestId: `r-${i}`, method: "host/browser.snapshot", phase: "reading", elapsedMs: i });
  let result = s.diagnostics.read(a);
  assert.equal(result.truncated, true); assert.equal(result.entries.length, 512);
  assert.ok(Buffer.byteLength(JSON.stringify(result.entries)) <= 256 * 1024);
  assert.equal(s.diagnostics.read(b).entries.length, 0);
  s.diagnostics.dispose();
});

test("live grant attribution survives more than 256 bindings and page reopen", async () => {
  const s = setup();
  s.diagnostics.bind(s.grants.install({ grantId: "first", taskId: "first", sessionId: "s", diagnosticScope: a }));
  const closed = await s.surfaces.open("https://example.test", { taskId: "first", sessionId: "s", temporary: false });
  s.surfaces.close(closed.id);
  for (let i = 0; i < 256; i++) s.diagnostics.bind(s.grants.install({ grantId: `g-${i}`, taskId: `task-${i}`, sessionId: "s", diagnosticScope: b }));
  assert.equal(s.grants.verify("first").diagnosticScope, a);
  const tab = await s.surfaces.open("https://example.test", { taskId: "first", sessionId: "s", temporary: false });
  const buffer = tab.view.diagnostics = new DiagnosticBuffer();
  s.diagnostics.read(a);
  buffer.add({ kind: "exception", message: "retained after 257 grants" });
  const result = s.diagnostics.read(a);
  assert.equal(result.truncated, false);
  assert.ok(result.entries.some(row => row.kind === "page" && row.message === "retained after 257 grants"));
  assert.equal(result.tabs[0]?.pageDiagnosticsAvailable, true);
  assert.equal(s.diagnostics.read(b).entries.some(row => row.tabId === tab.id), false);
  s.grants.revoke("first");
  buffer.add({ kind: "console", message: "existing page remains attributable after revoke" });
  assert.ok(s.diagnostics.read(a).entries.some(row => row.message === "existing page remains attributable after revoke"));
  s.surfaces.destroyAll(); s.diagnostics.dispose();
});

test("host export serializes sanitized console URLs for every protocol casing", async () => {
  const s = setup();
  await s.calls["host/browser.grant"]({ grantId: "a", tabId: "a", sessionId: "s", diagnosticScope: a });
  const tab = await s.surfaces.open("https://example.test", { taskId: "a", sessionId: "s", temporary: false });
  const buffer = tab.view.diagnostics = new DiagnosticBuffer();
  s.diagnostics.read(a);
  for (const scheme of ["https", "HTTPS", "HtTpS", "HTTP"]) {
    buffer.add({ kind: "console", message: `Request failed ${scheme}://review-user:REVIEW-PASSWORD@example.test/callback?code=REVIEW-OAUTH-CODE&signature=REVIEW-SIGNATURE#REVIEW-FRAGMENT`, url: `${scheme}://example.test/source?code=REVIEW-OAUTH-CODE` });
  }
  const exported = await s.calls["host/browser.exportDiagnostics"]({ scope: a });
  const json = JSON.stringify(exported);
  for (const secret of ["review-user", "REVIEW-PASSWORD", "REVIEW-OAUTH-CODE", "REVIEW-SIGNATURE", "REVIEW-FRAGMENT"]) assert.equal(json.includes(secret), false, secret);
  const entries = (exported as ReturnType<BrowserDiagnosticExport["read"]>).entries;
  assert.deepEqual(entries.filter(row => row.kind === "page").map(row => ({ message: row.message, url: row.url })), [
    ...Array.from({ length: 3 }, () => ({ message: "Request failed https://example.test/callback", url: "https://example.test/source" })),
    { message: "Request failed http://example.test/callback", url: "http://example.test/source" },
  ]);
  s.surfaces.destroyAll(); s.diagnostics.dispose();
});
