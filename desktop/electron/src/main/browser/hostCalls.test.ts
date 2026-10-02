import assert from "node:assert/strict";
import { test } from "node:test";
import { RpcError } from "../rpc.js";
import { ActionExecutor } from "./actions.js";
import { DocumentRegistry } from "./documents.js";
import { DownloadTracker } from "./downloads.js";
import { BROWSER_ERR_NO_GRANT, BROWSER_ERR_STALE_REFERENCE, BROWSER_ERR_TAKEN_OVER } from "./errors.js";
import { FakeViewFactory, silentLog } from "./fakeGuestViews.js";
import { GrantRegistry } from "./grants.js";
import { buildBrowserHostCalls, type HostBrowserTab, type BrowserHostDeps } from "./hostCalls.js";
import { BrowserSurfaceManager } from "./surfaceManager.js";

const code = (value: number) => (error: unknown) => error instanceof RpcError && error.code === value;

async function setup(overrides: Partial<BrowserHostDeps> = {}) {
  const factory = new FakeViewFactory();
  const surfaces = new BrowserSurfaceManager({ views: factory, contentSize: () => null, onTakeover() {}, onCrash() {}, log: silentLog, openWaitMs: 5 });
  const grants = new GrantRegistry({ generation: () => "gen-1" });
  let tokens = 0;
  const documents = new DocumentRegistry(() => `tok-${++tokens}`);
  const actions = new ActionExecutor({ surfaces, documents });
  const directories = new Map<string, string>();
  const downloads = new DownloadTracker({
    tabForWebContents: () => undefined,
    defaultDirectory: (taskId) => `/dl/${taskId}`,
    onUpdate() {},
    log: silentLog,
  });
  const table = buildBrowserHostCalls({
    surfaces,
    grants,
    documents,
    actions,
    downloads,
    snapshot: async (tab, selector) => ({ tabId: tab.id, selector }) as never,
    screenshot: async (tab, request) => ({ tabId: tab.id, ...request }) as never,
    ...overrides,
  });
  const call = async (method: keyof typeof table, params: Record<string, unknown> = {}): Promise<unknown> => table[method](params);
  const setDirs = downloads.setTaskDirectory.bind(downloads);
  downloads.setTaskDirectory = (taskId, directory) => {
    directories.set(taskId, directory);
    setDirs(taskId, directory);
  };
  return { surfaces, grants, documents, downloads, directories, table, call };
}

test("grant, list and open are scoped to the grant's task", async () => {
  const s = await setup();
  await assert.rejects(s.call("host/browser.tabs.list", { grantId: "g" }), code(BROWSER_ERR_NO_GRANT));
  await s.call("host/browser.grant", { grantId: "g", tabId: "task-1", sessionId: "s1" });
  await s.call("host/browser.grant", { grantId: "h", tabId: "task-2", sessionId: "s2" });

  const opened = (await s.call("host/browser.tabs.open", { grantId: "g", url: "https://a.test" })) as HostBrowserTab;
  assert.equal(opened.url, "https://a.test/");
  assert.equal(s.surfaces.require(opened.id).taskId, "task-1", "the tab belongs to the grant's task");
  await s.surfaces.open("https://b.test", { taskId: "task-2", temporary: false });

  const listed = (await s.call("host/browser.tabs.list", { grantId: "g" })) as { tabs: HostBrowserTab[] };
  assert.deepEqual(listed.tabs.map((tab) => tab.id), [opened.id], "another task's tabs are invisible");
});

test("revoking a grant reclaims a pending open without closing a user-taken-over page", async () => {
  for (const takeover of [false, true]) {
    const s = await setup();
    await s.call("host/browser.grant", { grantId: "g", tabId: "task", sessionId: "session" });
    const originalOpen = s.surfaces.open.bind(s.surfaces);
    s.surfaces.open = async (...args) => {
      const promise = originalOpen(...args);
      const tab = s.surfaces.all()[0];
      if (takeover) s.surfaces.takeover(tab.id, "user click");
      await s.call("host/browser.revoke", { grantId: "g" });
      return promise;
    };
    await assert.rejects(s.call("host/browser.tabs.open", { grantId: "g", url: "https://a.test", requestId: "pending" }),
      /outcome is unknown/);
    assert.equal(s.surfaces.all().length, takeover ? 1 : 0);
    s.surfaces.destroyAll();
  }
});

test("an open that finishes just before grant revocation cannot leave an unclaimed agent page", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "g", tabId: "task", sessionId: "session" });
  const originalOpen = s.surfaces.open.bind(s.surfaces);
  s.surfaces.open = async (...args) => {
    const tab = await originalOpen(...args);
    await s.call("host/browser.revoke", { grantId: "g" });
    return tab;
  };
  await assert.rejects(s.call("host/browser.tabs.open", { grantId: "g", url: "https://a.test" }),
    /outcome is unknown/);
  assert.equal(s.surfaces.all().length, 0);
});

test("cancel and takeover release a capture lease before a late capture reply", async () => {
  for (const reason of ["cancel", "takeover", "revoke", "resize", "close"]) {
    let respond!: (value: never) => void;
    let entered!: () => void;
    const started = new Promise<void>(resolve => { entered = resolve; });
    const s = await setup({ screenshot: async () => { entered(); return new Promise(resolve => { respond = resolve; }); } });
    await s.call("host/browser.grant", { grantId: "g", tabId: "task", sessionId: "s" });
    const tab = await s.surfaces.open("https://test.example", { taskId: "task", sessionId: "s", temporary: false });
    let released = 0;
    tab.view.prepareCapture = async () => () => { released++; };
    const request = s.call("host/browser.screenshot", { grantId: "g", tabId: tab.id, requestId: "r1" });
    await started;
    if (reason === "cancel") await s.call("host/browser.cancel", { grantId: "g", requestId: "r1" });
    if (reason === "takeover") s.surfaces.takeover(tab.id, "user");
    if (reason === "revoke") s.grants.revoke("g");
    if (reason === "resize") s.surfaces.setLayout({ x: 0, y: 0, width: 700, height: 500 });
    if (reason === "close") s.surfaces.close(tab.id);
    await assert.rejects(request);
    assert.equal(released, 1, reason);
    respond({ path: "late.png" } as never);
    await Promise.resolve();
    assert.equal(released, 1, "late completion cannot release another request's lease");
  }
});

test("operation trace records failure phase and timing without page contents or credentials", async () => {
  const trace: unknown[] = [];
  const s = await setup({ trace: event => trace.push(event), snapshot: async () => { throw new RpcError(-32013, "password=DO_NOT_LOG https://example.test/?token=DO_NOT_LOG", { kind: "script_runtime_error" }); } });
  await s.call("host/browser.grant", { grantId: "g", tabId: "task", sessionId: "s" });
  const tab = await s.surfaces.open("https://example.test/?token=DO_NOT_LOG", { taskId: "task", sessionId: "s", temporary: false });
  await assert.rejects(s.call("host/browser.snapshot", { grantId: "g", tabId: tab.id, requestId: "trace" }));
  const final = trace.at(-1) as { phase: string; elapsedMs: number; errorKind: string };
  assert.equal(final.phase, "failed"); assert.equal(final.errorKind, "script_runtime_error"); assert.ok(final.elapsedMs >= 0);
  assert.equal(JSON.stringify(trace).includes("DO_NOT_LOG"), false);
  assert.equal(JSON.stringify(trace).includes("example.test"), false);
});

test("panel resize preserves fixed CSS observations but invalidates natural viewport observations", async () => {
  for (const fixed of [true, false]) {
    let entered!: () => void, finish!: (value: never) => void;
    const started = new Promise<void>(resolve => { entered = resolve; });
    const s = await setup({ snapshot: async () => { entered(); return new Promise(resolve => { finish = resolve; }); } });
    await s.call("host/browser.grant", { grantId: "g", tabId: "task", sessionId: "s" });
    const tab = await s.surfaces.open("https://example.test", { taskId: "task", sessionId: "s", temporary: true });
    if (!fixed) tab.viewport = null;
    const before = tab.viewportRevision;
    const request = s.call("host/browser.snapshot", { grantId: "g", tabId: tab.id });
    const outcome = request.then(value => ({ value }), error => ({ error }));
    await started;
    s.surfaces.setLayout({ x: 0, y: 0, width: 700, height: 500 });
    finish({ tree: "fixture" } as never);
    const result = await outcome;
    if (fixed) { assert.deepEqual(result, { value: { tree: "fixture" } }); assert.equal(tab.viewportRevision, before); }
    else { assert.ok("error" in result); assert.ok(tab.viewportRevision > before); }
    s.surfaces.destroyAll();
  }
});

test("cancel releases a navigation waiter without replaying the dispatched navigation", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "g", tabId: "task", sessionId: "s" });
  const tab = await s.surfaces.open("https://test.example", { taskId: "task", sessionId: "s", temporary: false });
  let finish!: () => void;
  const page = tab.view.page as import("./fakeGuestViews.js").FakePage;
  page.loadResult = new Promise(resolve => { finish = resolve; });
  const request = s.call("host/browser.tabs.navigate", { grantId: "g", tabId: tab.id, requestId: "nav", url: "https://next.example" });
  await s.call("host/browser.cancel", { grantId: "g", requestId: "nav" });
  await assert.rejects(request, /outcome is unknown/);
  assert.equal(page.getURL(), "https://next.example/");
  finish();
  await Promise.resolve();
  assert.equal(s.surfaces.tabsForSession("task", "s").length, 1);
});

test("a new session grant cannot list or control the task's earlier session tabs", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "old", tabId: "task-1", sessionId: "session-1" });
  const old = (await s.call("host/browser.tabs.open", { grantId: "old", url: "https://old.test" })) as HostBrowserTab;
  await s.call("host/browser.grant", { grantId: "next", tabId: "task-1", sessionId: "session-2" });
  const listed = (await s.call("host/browser.tabs.list", { grantId: "next" })) as { tabs: HostBrowserTab[] };
  assert.deepEqual(listed.tabs, []);
  await assert.rejects(s.call("host/browser.snapshot", { grantId: "next", tabId: old.id }), code(BROWSER_ERR_NO_GRANT));
});

test("tab calls verify the grant and refuse tabs of another task", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "g", tabId: "task-1", sessionId: "" });
  const other = await s.surfaces.open("https://b.test", { taskId: "task-2", temporary: false });
  await assert.rejects(s.call("host/browser.tabs.navigate", { grantId: "g", tabId: other.id, url: "https://c.test" }), code(BROWSER_ERR_NO_GRANT));
  await assert.rejects(s.call("host/browser.tabs.close", { grantId: "g", tabId: other.id }), code(BROWSER_ERR_NO_GRANT));
  await assert.rejects(s.call("host/browser.tabs.navigate", { grantId: "g", tabId: "tab-99", url: "https://c.test" }), code(BROWSER_ERR_NO_GRANT));
});

test("reads and writes refuse a tab the user has taken over", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "g", tabId: "task-1", sessionId: "" });
  const tab = await s.surfaces.open("https://a.test", { taskId: "task-1", temporary: false });
  assert.deepEqual(await s.call("host/browser.snapshot", { grantId: "g", tabId: tab.id, selector: "" }), { tabId: tab.id, selector: "" });
  s.surfaces.takeover(tab.id, "user mousedown");
  await assert.rejects(s.call("host/browser.snapshot", { grantId: "g", tabId: tab.id }), code(BROWSER_ERR_TAKEN_OVER));
  await assert.rejects(s.call("host/browser.tabs.navigate", { grantId: "g", tabId: tab.id, url: "https://c.test" }), code(BROWSER_ERR_TAKEN_OVER));
  const refreshed = (await s.call("host/browser.tabs.navigate", {
    grantId: "g", tabId: tab.id, url: "https://refreshed.test", allowHuman: true,
  })) as HostBrowserTab;
  assert.equal(refreshed.url, "https://refreshed.test/");
  assert.equal(s.surfaces.require(tab.id).mode, "human", "an explicit user refresh preserves takeover");
  await assert.rejects(s.call("host/browser.screenshot", { grantId: "g", tabId: tab.id, ref: "", directory: "" }), code(BROWSER_ERR_TAKEN_OVER));
  const closed = await s.call("host/browser.tabs.close", { grantId: "g", tabId: tab.id });
  assert.deepEqual(closed, {}, "closing is still allowed so the task can clean up");
  assert.equal(s.surfaces.get(tab.id), undefined);
});

test("act re-verifies the grant and rejects a stale document token", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "g", tabId: "task-1", sessionId: "" });
  const tab = await s.surfaces.open("https://a.test", { taskId: "task-1", temporary: false });
  await assert.rejects(
    s.call("host/browser.act", { grantId: "g", tabId: tab.id, documentToken: "nope", action: "click", ref: "e1" }),
    code(BROWSER_ERR_STALE_REFERENCE),
  );
  await s.call("host/browser.revoke", { grantId: "g" });
  await assert.rejects(
    s.call("host/browser.act", { grantId: "g", tabId: tab.id, documentToken: "nope", action: "click", ref: "e1" }),
    code(BROWSER_ERR_NO_GRANT),
  );
});

test("act and screenshot register the scratch directory for downloads", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "g", tabId: "task-1", sessionId: "" });
  const tab = await s.surfaces.open("https://a.test", { taskId: "task-1", temporary: false });
  await s.call("host/browser.screenshot", { grantId: "g", tabId: tab.id, ref: "", directory: "/scratch/one" });
  assert.equal(s.directories.get("task-1"), "/scratch/one");
  await s.call("host/browser.screenshot", { grantId: "g", tabId: tab.id, ref: "", directory: "" });
  assert.equal(s.directories.get("task-1"), "/scratch/one", "an empty directory keeps the previous mapping");
});

test("downloads waits are served per tab and a zero wait answers at once", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "g", tabId: "task-1", sessionId: "" });
  const tab = await s.surfaces.open("https://a.test", { taskId: "task-1", temporary: false });
  assert.deepEqual(await s.call("host/browser.downloads", { grantId: "g", tabId: tab.id, waitForMs: 0 }), { downloads: [] });
});

test("revoke invalidates the task's document tokens", async () => {
  const s = await setup();
  await s.call("host/browser.grant", { grantId: "g", tabId: "task-1", sessionId: "" });
  const tab = await s.surfaces.open("https://a.test", { taskId: "task-1", temporary: false });
  const token = s.documents.issue({ tabId: tab.id, epoch: tab.epoch, snapshotId: "snap", frames: [] });
  assert.ok(s.documents.lookup(token));
  await s.call("host/browser.revoke", { grantId: "g" });
  assert.equal(s.documents.lookup(token), undefined);
});
