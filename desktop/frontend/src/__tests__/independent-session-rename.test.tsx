import assert from "node:assert/strict";
import { test } from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { desktopProjectAdapter } from "../app-runtime/desktopProjectAdapter";
import { useHistoryCommands } from "../app-runtime/useHistoryCommands";
import { useProjectTopicCommands } from "../app-runtime/useProjectTopicCommands";
import { renameProjectTopic, type ProjectTopicPorts } from "../app-runtime/projectTopicOwner";
import type { RemoteSessionView } from "../lib/remoteTypes";
import type { HistoryViewState } from "../app-runtime/historyViewProjection";
import type { SessionMeta } from "../lib/types";
import type { SessionSelector } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";

const sharedTopic = "shared-topic";
function savedSession(id: string, canonical: boolean): SessionMeta {
  return {
    path: `/sessions/${id}.jsonl`, sessionId: canonical ? id : undefined,
    hostId: "local", topicId: sharedTopic, title: `Original ${id}`, preview: id,
    turns: 1, createdAt: 1, lastActivityAt: 1, modTime: 1, current: false, open: false,
    scope: "project", workspaceRoot: "/repo",
  };
}

function environment(sessions: SessionMeta[], gate?: Promise<void>) {
  const dom = new JSDOM("<div id='root'></div>");
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const root = createRoot(document.getElementById("root")!);
  const requests: { selector: SessionSelector; title: string }[] = [];
  let bulkCalls = 0;
  const host = installDesktopHostStub({
    RenameTopic: async (topicId: string, title: string) => {
      bulkCalls++;
      await gate;
      for (const session of sessions) if (session.topicId === topicId) session.title = title;
    },
    RenameSessionTarget: async (selector: SessionSelector, title: string) => {
      requests.push({ selector, title });
      await gate;
      const target = selector.ref
        ? sessions.find(session => session.sessionId === selector.ref?.sessionId && session.hostId === selector.ref?.hostId)
        : sessions.find(session => session.path === selector.sessionPath);
      assert.ok(target, "rename must resolve the explicit saved session");
      target.title = title;
      return { applied: true, targetKey: target.path };
    },
  });
  return { dom, root, requests, bulkCalls: () => bulkCalls, close: async () => {
    await act(async () => root.unmount()); host.uninstall(); dom.window.close();
  } };
}

for (const canonical of [true, false]) {
  test(`history rename isolates B from same-topic A (${canonical ? "canonical" : "legacy source"})`, async () => {
    const sessions = [savedSession("a", canonical), savedSession("b", canonical)];
    const env = environment(sessions);
    let commands!: ReturnType<typeof useHistoryCommands>;
    let view: HistoryViewState | null = { kind: "history", source: "all", sessions };
    function Probe() {
      commands = useHistoryCommands({ running: false,
        setHistView: update => { view = typeof update === "function" ? update(view) : update; },
        ports: {
          listSessions: async () => sessions,
          deleteSession: async () => {},
          renameSession: async () => { throw new Error("history must use the explicit target API"); },
          openPage: () => {},
        },
      });
      return null;
    }
    try {
      await act(async () => env.root.render(<Probe />));
      await act(async () => commands.onRenameHistorySession(sessions[1]!, "Only B"));
      assert.equal(sessions[0]!.title, "Original a");
      assert.equal(sessions[1]!.title, "Only B");
      assert.equal(env.bulkCalls(), 0, "single-session history UI must never call the topic bulk API");
      assert.equal(env.requests.length, 1);
      if (canonical) assert.deepEqual(env.requests[0]!.selector.ref, { hostId: "local", sessionId: "b" });
      else assert.equal(env.requests[0]!.selector.sessionPath, "/sessions/b.jsonl");
    } finally { await env.close(); }
  });
}

test("top title rename keeps B as its target when the active session changes to same-topic A", async () => {
  let complete!: () => void;
  const gate = new Promise<void>(resolve => { complete = resolve; });
  const sessions = [savedSession("a", true), savedSession("b", true)];
  const env = environment(sessions, gate);
  let commands!: ReturnType<typeof useProjectTopicCommands>;
  let activeSyncs = 0;
  const ports = { ...desktopProjectAdapter,
    markChanged: () => {}, refreshTabs: async () => [], syncActive: async () => { activeSyncs++; },
  };
  function Probe({ id }: { id: string }) {
    const target = { kind: "local" as const, topicId: sharedTopic,
      selector: { ref: { hostId: "local", sessionId: id } } };
    commands = useProjectTopicCommands({
      visible: { tabId: `tab-${id}`, sessionKey: id },
      topic: { id: sharedTopic, title: `Original ${id}`, target }, ports,
      navigation: { openBlank: async () => {}, enqueue: async () => {}, switchFolder: async () => {} },
      reportError: error => { throw error; },
    });
    return null;
  }
  try {
    await act(async () => env.root.render(<Probe id="b" />));
    await act(async () => commands.startActiveTopicRename());
    await act(async () => commands.setTopicTitleDraft("Only B"));
    let pending!: Promise<void>;
    await act(async () => { pending = commands.commitActiveTopicRename(); });
    await act(async () => env.root.render(<Probe id="a" />));
    await act(async () => { complete(); await pending; });
    assert.equal(sessions[0]!.title, "Original a");
    assert.equal(sessions[1]!.title, "Only B");
    assert.equal(env.bulkCalls(), 0);
    assert.deepEqual(env.requests.map(request => request.selector.ref), [{ hostId: "local", sessionId: "b" }]);
    assert.equal(commands.topicbarEditing, false);
    assert.equal(activeSyncs, 0, "B's completion cannot replace the newly selected A");
  } finally { complete(); await env.close(); }
});

function remoteRenameFixture(sessions: RemoteSessionView[]) {
  const writes: { host: string; workspace: string; name: string; title: string }[] = [];
  const ports: ProjectTopicPorts = {
    renameLocal: async () => { throw new Error("remote targets must never reach the local owner"); },
    listRemote: async () => sessions,
    renameRemote: async (host, workspace, name, title) => { writes.push({ host, workspace, name, title }); },
    markChanged: () => {}, refreshTabs: async () => [], syncActive: async () => {},
  };
  const authority = { checkpoint() {}, ownsUI: () => true };
  return { writes, rename: (sessionId: string) => renameProjectTopic({
    ports, title: "Only B", target: {
      kind: "remote", hostId: "host-a", workspace: "/repo", sessionPath: "/shared/history.jsonl", sessionId,
    },
  }, authority) };
}

test("remote top rename resolves sessionId before an equal path on another session", async () => {
  const fixture = remoteRenameFixture([
    { name: "a", sessionId: "a", path: "/shared/history.jsonl", title: "A", turns: 1 },
    { name: "b", sessionId: "b", path: "/shared/history.jsonl", title: "B", turns: 1 },
  ]);
  await fixture.rename("b");
  assert.deepEqual(fixture.writes, [{ host: "host-a", workspace: "/repo", name: "b", title: "Only B" }]);
});

test("remote top rename rejects a missing explicit sessionId instead of falling back to its old path", async () => {
  const fixture = remoteRenameFixture([
    { name: "a", sessionId: "a", path: "/shared/history.jsonl", title: "A", turns: 1 },
  ]);
  await assert.rejects(fixture.rename("b"));
  assert.deepEqual(fixture.writes, []);
});

test("remote top rename rejects duplicate protocol names even when sessionId resolves one row", async () => {
  const fixture = remoteRenameFixture([
    { name: "duplicate", sessionId: "a", path: "/other/history.jsonl", title: "A", turns: 1 },
    { name: "duplicate", sessionId: "b", path: "/shared/history.jsonl", title: "B", turns: 1 },
  ]);
  await assert.rejects(fixture.rename("b"));
  assert.deepEqual(fixture.writes, [], "name-only protocol writes cannot safely distinguish these rows");
});
