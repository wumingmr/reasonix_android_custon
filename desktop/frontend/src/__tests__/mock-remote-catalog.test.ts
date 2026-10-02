import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { app } from "../lib/bridge";

const dom = new JSDOM("", { url: "http://localhost/?mock=bench" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, localStorage: dom.window.localStorage });
try {
  const local = (await app.ListTabs())[0];
  const remote = await app.OpenRemoteProjectTab("demo", "~/app", { sessionName: "intro" });
  assert.equal(remote.sessionPath, "~/app/sessions/intro.jsonl");
  assert.ok((await app.ListTabs()).some(tab => tab.id === remote.id), "remote open and ListTabs share the backend catalog");
  await app.SetActiveTab(remote.id);
  assert.deepEqual((await app.ListTabs()).filter(tab => tab.active).map(tab => tab.id), [remote.id]);
  await app.SetRemoteTabModel(remote.id, "fixture/model");
  assert.equal((await app.ListTabs()).find(tab => tab.id === remote.id)?.label, "fixture/model");
  const renewed = await app.OpenRemoteProjectTab("demo", "~/app", { newSession: true });
  assert.equal(renewed.id, remote.id, "remote new session reuses its workspace surface");
  assert.ok(renewed.sessionPath && renewed.sessionPath !== remote.sessionPath, "new session replaces the durable session identity");
  assert.equal((await app.ListTabs()).filter(tab => tab.id === remote.id).length, 1);
  assert.equal((await app.ListTabs()).find(tab => tab.id === remote.id)?.topicTitle, "New session");
  const status = await app.RemoteTabStatus(remote.id) as Record<string, unknown>;
  assert.equal(status.sessionPath, renewed.sessionPath);
  assert.equal(status.plan, false);
  assert.equal(status.toolApprovalMode, "workspace-write");
  assert.equal(status.goal, "");
  assert.deepEqual((await app.RemoteTabSnapshot(remote.id)).status, status, "snapshot and status share the authoritative composer profile");
  // A canonical row carries only its immutable session id: opening it must
  // bind that identity on the tab (the Serve publishes TabMeta.sessionId from
  // opts.SessionID) instead of falling back to a synthesized basename route.
  const canonical = await app.OpenRemoteProjectTab("demo", "~/app", { sessionId: "canonical-session-1" });
  assert.equal(canonical.id, remote.id, "a canonical session reuses its workspace surface");
  assert.equal(canonical.sessionId, "canonical-session-1", "opening by session id binds the canonical identity");
  assert.ok(!canonical.sessionPath?.includes("intro"), "a canonical open does not inherit the previous legacy route");
  const legacy = await app.OpenRemoteProjectTab("demo", "~/app", { sessionName: "intro" });
  assert.equal(legacy.sessionPath, "~/app/sessions/intro.jsonl", "legacy rows keep their basename route");
  assert.ok((await app.RemoteProjectSessions("demo", "~/app")).find(row => row.name === "intro")?.current,
    "the opened legacy session becomes the current row");
  await app.SetActiveTab(local.id);
  assert.deepEqual((await app.ListTabs()).filter(tab => tab.active).map(tab => tab.id), [local.id]);
  await app.CloseRemoteTab(remote.id);
  assert.ok(!(await app.ListTabs()).some(tab => tab.id === remote.id));
  await assert.rejects(app.SetActiveTab(remote.id), /not found/);
  console.log("mock remote catalog: open, refresh, model, new session, selection and close agree");
} finally { dom.window.close(); }
