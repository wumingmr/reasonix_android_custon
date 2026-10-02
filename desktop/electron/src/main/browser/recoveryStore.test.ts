import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { BrowserRecoveryStore, type RecoveredTab } from "./recoveryStore.js";
import { LazyGuestView } from "./lazyGuestView.js";
import { FakeViewFactory } from "./fakeGuestViews.js";

const tab: RecoveredTab = { id: "tab-2", taskId: "task", sessionId: "session", url: "https://example.test/", title: "Page", viewport: null };
test("recovery preserves future fields but never capabilities and rejects expired preview URLs", () => {
  const directory = mkdtempSync(join(tmpdir(), "browser-recovery-")), path = join(directory, "tabs.json");
  try {
    writeFileSync(path, JSON.stringify({ version: 1, future: 42, tabs: [{ ...tab, custom: true, grantId: "old", refs: [1], password: "secret" }, { ...tab, id: "tab-3", url: "http://localhost/__reasonix_workspace_media/expired" }] }));
    const store = new BrowserRecoveryStore(path, assert.fail);
    assert.equal(store.tabs.length, 1);
    store.save(store.tabs);
    const saved = JSON.parse(readFileSync(path, "utf8"));
    assert.equal(saved.future, 42); assert.equal(saved.tabs[0].custom, true);
    for (const key of ["grantId", "refs", "password"]) assert.equal(saved.tabs[0][key], undefined);
    writeFileSync(path, '{"version":999,"future":"untouched"}');
    const future = new BrowserRecoveryStore(path, assert.fail); future.save([tab]);
    assert.equal(readFileSync(path, "utf8"), '{"version":999,"future":"untouched"}');
  } finally { rmSync(directory, { recursive: true, force: true }); }
});
test("recovered tabs allocate no page until access and share one initial load", async () => {
  const factory = new FakeViewFactory();
  let creates = 0;
  const lazy = new LazyGuestView({ create: partition => { creates++; return factory.create(partition); } }, "persist:browser", tab);
  assert.equal(lazy.page.id, -1); assert.equal(lazy.page.getURL(), tab.url);
  lazy.setBounds({ x: 0, y: 0, width: 800, height: 600 }); lazy.setVisible(false);
  assert.equal(creates, 0);
  await Promise.all([lazy.ensureLoaded(), lazy.ensureLoaded()]);
  assert.equal(creates, 1); assert.equal(lazy.isPlaceholder(), false);
  lazy.destroy();
  const unopened = new LazyGuestView(factory, "persist:browser", tab); unopened.destroy();
  assert.throws(() => unopened.ensureLoaded(), /closed/);
});

test("file preview recovery persists source references without expiring bearer URLs", async () => {
  const directory = mkdtempSync(join(tmpdir(), "preview-recovery-")), path = join(directory, "tabs.json");
  try {
    const reference = { source: "workspace" as const, path: "index.html", toolCallId: "" };
    const store = new BrowserRecoveryStore(path, assert.fail);
    store.save([{ ...tab, url: "http://localhost/__reasonix_workspace_media/SECRET", fileReference: reference }]);
    assert.equal(readFileSync(path, "utf8").includes("SECRET"), false);
    const loaded = new BrowserRecoveryStore(path, assert.fail);
    assert.deepEqual(loaded.tabs[0].fileReference, reference);
    const lazy = new LazyGuestView(new FakeViewFactory(), "persist:browser", loaded.tabs[0]);
    await assert.rejects(lazy.ensureLoaded(), /fresh file authorization/);
    assert.equal(lazy.isPlaceholder(), true);
  } finally { rmSync(directory, { recursive: true, force: true }); }
});
