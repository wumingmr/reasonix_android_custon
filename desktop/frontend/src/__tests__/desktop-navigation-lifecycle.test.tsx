import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useDesktopNavigation } from "../app-runtime/useDesktopNavigation";
import type { DesktopNavigationPorts } from "../app-runtime/desktopNavigationOwner";
import type { SessionMeta, TabMeta } from "../lib/types";
import type { SidebarImConnection } from "../app-runtime/sidebarImProjection";
import type { Translator } from "../lib/i18n";
import { __emitMockRemoteTabOpened } from "../lib/remoteTabEvents";
import { useRemoteTabOpened } from "../lib/useRemoteTabOpened";
import { projectTreeTopicOpenRequest } from "../lib/projectTreeTopic";

function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
const dom = new JSDOM("<div id='root'></div>");
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
const root = createRoot(document.getElementById("root")!);
const tab = (id: string) => ({ id, label: id } as TabMeta);
const pending = new Map<string, ReturnType<typeof deferred<TabMeta>>>();
const calls: string[] = [];
const acceptedTopics: number[] = [];
let intent = 0;
let registration: ReturnType<typeof deferred<string>> | undefined;
let preparationReads = 0;
let lastTopicPath = "";
let api!: ReturnType<typeof useDesktopNavigation>;
const activate = (id: string) => { calls.push(`open:${id}`); const request = deferred<TabMeta>(); pending.set(id, request); return request.promise; };
const ports: Parameters<typeof useDesktopNavigation>[0]["ports"] = {
  isNavigationIntentCurrent: seq => seq === intent,
  registeredNavigationIntent: async seq => registration ? registration.promise : String(seq),
  openRemoteProject: async (_host, workspace) => activate(`remote:${workspace}`),
  switchRemoteTab: async (meta, seq) => { calls.push(`remote-switch:${meta.id}:${seq}`); },
  activateTopic: async (_scope, _workspace, id, path) => { lastTopicPath = path; return activate(id); },
  openCanonicalSession: async (ref) => { await activate(`canonical:${ref.sessionId}`); },
  openTopicSession: async (_scope, _workspace, id) => { calls.push("tab-session"); return activate(id); },
  openGlobalTab: async id => { calls.push("tab-global"); return activate(id); },
  openProjectTab: async (_workspace, id) => { calls.push("tab-project"); return activate(id); },
  ensureBlankSurface: async (_scope, workspace) => activate(`blank:${workspace}`),
  ensureBlankTab: async (_scope, workspace) => { calls.push("tab-blank"); return activate(`blank:${workspace}`); },
  createIsolatedWorktree: async workspace => ({ tab: await activate(`worktree:${workspace}`), branch: "fixture", sourceDirty: true }) as Awaited<ReturnType<DesktopNavigationPorts["createIsolatedWorktree"]>>,
  openChannelSession: async (path, id) => { calls.push(`channel:${id}:${path}`); },
  resumeSession: async (path, id) => { calls.push(`resume:${id}:${path}`); },
  listTabs: async () => [], applyTabs: () => { calls.push("tabs"); }, seedTab: value => { calls.push(`seed:${value.id}`); },
  listSessions: async () => { calls.push("history-refresh"); return []; },
  prepareSession: async selector => { calls.push(`prepare:${selector.source?.sourceKey}`); return { operationId: "prepare-legacy", sourceKey: selector.source?.sourceKey || "", status: "queued", revision: 1, retryable: false }; },
  getSessionPreparation: async () => {
    preparationReads++;
    return preparationReads < 2
      ? { operationId: "prepare-legacy", sourceKey: "legacy", status: "preparing", revision: 2, retryable: false }
      : { operationId: "prepare-legacy", sourceKey: "legacy", status: "ready", revision: 3, target: { hostId: "local", sessionId: "prepared-target" }, retryable: false };
  },
  topicAccepted: seq => { acceptedTopics.push(seq); },
};
function Probe({ visible = "A" }: { visible?: string }) {
  useRemoteTabOpened(meta => { calls.push(`resource:${meta.id}`); }, () => {});
  api = useDesktopNavigation({ visible: { tabId: visible, sessionKey: visible }, ports,
    setTabRevealSignal: () => { calls.push("reveal-tab"); },
    setProjectRevision: () => { calls.push("project"); }, setHistory: () => { calls.push("history-close"); },
    t: ((key: string) => key) as Translator, showToast: message => { calls.push(`notice:${message}`); },
    noteIntent: () => ++intent, beginSurface: seq => { calls.push(`begin:${seq}`); },
    settleSurface: seq => { if (seq === intent) calls.push(`settle:${seq}`); }, showChat: () => {},
  });
  return null;
}
const paint = (visible = "A") => act(async () => root.render(<Probe visible={visible} />));
const topic = (id: string) => api.enqueueNavigation({ kind: "topic", scope: "project", workspaceRoot: "fixture", topicId: id });
async function finish(id: string, task: Promise<void>) {
  const deadline = Date.now() + 5000;
  while (!pending.has(id) && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 1));
  assert.ok(pending.has(id), `navigation reached ${id}`);
  pending.get(id)!.resolve(tab(id)); await task;
}
try {
  await paint();
  const canonical = api.enqueueNavigation({ kind: "canonical-session", ref: { hostId: "local", sessionId: "canonical-target" } });
  await finish("canonical:canonical-target", canonical);
  assert.ok(calls.includes("history-close") && calls.includes("tabs") && calls.includes("reveal-tab"));
  calls.length = 0; intent = 0;
  const entry = api.enqueueNavigation;
  const a = topic("A"), b = topic("B"), c = topic("C");
  await b;
  assert.deepEqual(calls.filter(value => value.startsWith("open:")), ["open:A"]);
  await finish("A", a);
  assert.deepEqual(calls.filter(value => value.startsWith("open:")), ["open:A", "open:C"]);
  await finish("C", c);
  assert.deepEqual(calls.filter(value => value.startsWith("seed:")), ["seed:C"]);
  assert.deepEqual(acceptedTopics, [3], "only the accepted queue target can release an automation link");
  assert.deepEqual(calls.filter(value => value.startsWith("settle:")), ["settle:3"], "old finally cannot settle the current surface");
  calls.length = 0;
  const stale = topic("stale"); intent++;
  await paint("B"); await paint("A");
  assert.equal(api.enqueueNavigation, entry);
  await finish("stale", stale);
  assert.deepEqual(calls.filter(value => /^(seed|tabs|notice|reveal|settle)/.test(value)), [], "ABA never restores old UI rights");

  calls.length = 0;
  const connection = { sessionId: "path:channel.jsonl", sessionSource: "auto", scope: "project", workspaceRoot: "im", title: "fixture" } as SidebarImConnection;
  const im = api.enqueueNavigation({ kind: "sidebar-im", connection });
  intent++;
  await finish("blank:im", im);
  assert.ok(!calls.some(value => value.startsWith("channel:")), "cancellation between blank activation and hydrate prevents a second mutation");
  calls.length = 0;
  const validIM = api.enqueueNavigation({ kind: "sidebar-im", connection });
  await finish("blank:im", validIM);
  assert.ok(calls.includes("channel:blank:im:channel.jsonl"));

  calls.length = 0;
  const botConnection = { ...connection, sessionId: "session:local:bot-conversation" };
  const botNavigation = api.enqueueNavigation({ kind: "sidebar-im", connection: botConnection });
  await finish("", botNavigation);
  assert.equal(lastTopicPath, "bot-session:local:bot-conversation");
  assert.ok(!calls.some(value => value.startsWith("open:blank:") || value.startsWith("channel:")),
    "canonical bot sessions open through the shared runtime surface, not a new blank tab");

  calls.length = 0;
  const isolated = api.enqueueNavigation({ kind: "isolated-worktree", workspaceRoot: "dirty" });
  await paint(); // Normal commits do not change the request epoch.
  await finish("worktree:dirty", isolated);
  assert.ok(calls.includes("notice:projectTree.worktreeCreatedDirty"));
  assert.ok(calls.includes("project"));

  calls.length = 0;
  const history = api.enqueueNavigation({ kind: "resume-session", session: { scope: "global", topicId: "history", path: "history.jsonl" } as SessionMeta });
  await finish("history", history);
  assert.ok(calls.includes("open:history"), "resuming a session activates its topic surface");
  assert.ok(!calls.includes("tab-session"), "every layout style takes the surface path, never a legacy tab");
  assert.ok(calls.includes("history-close"));

  calls.length = 0; preparationReads = 0;
  const legacy = api.enqueueNavigation({ kind: "resume-session", session: { scope: "global", topicId: "legacy-topic", title: "Legacy", path: "legacy.jsonl",
    source: { hostId: "local", sourceKey: "legacy", path: "legacy.jsonl" } } as SessionMeta });
	await finish("legacy-topic", legacy);
	assert.ok(!calls.includes("prepare:legacy"), "legacy navigation opens the source without implicit conversion");
	assert.equal(preparationReads, 0, "normal navigation never polls a migration task");

  calls.length = 0; preparationReads = 0;
  const sidebarRequest = projectTreeTopicOpenRequest({ kind: "global_topic", key: "cold-v4", label: "Cold v4",
    topicId: "cold-topic", source: { hostId: "local", sourceKey: "cold-v4", path: "cold-store" } });
  assert.ok(sidebarRequest?.sessionPath?.startsWith("session-source:"), "headless canonical sources keep their explicit identity");
  const sidebar = api.enqueueNavigation({ kind: "topic", ...sidebarRequest! });
  const sidebarIntent = intent;
	await finish("cold-topic", sidebar);
	assert.ok(!calls.includes("prepare:cold-v4"), "sidebar opens the historical source without implicit conversion");
  assert.equal(preparationReads, 0);
  assert.equal(acceptedTopics.at(-1), sidebarIntent, "prepared sidebar navigation retains topic acceptance");

  calls.length = 0;
  const conflicting = api.enqueueNavigation({ kind: "resume-session", session: {
    scope: "project", workspaceRoot: "D:\\J\\GIT\\ai-workspace", topicId: "conflicting-history",
    path: "history.jsonl", source: { hostId: "local", sourceKey: "conflicting", path: "history.jsonl" },
  } as SessionMeta });
  pending.get("conflicting-history")!.reject(new Error("session_operation:source_ambiguous:conflicting receipts"));
  await conflicting;
  assert.deepEqual(calls.filter(value => value.startsWith("notice:")), ["notice:projectTree.sessionError.sourceAmbiguous"],
    "migration failures must not claim the project directory does not exist");

  calls.length = 0;
  const failed = topic("failed");
  pending.get("failed")!.reject(new Error("fixture failure")); await failed;
  assert.deepEqual(calls.filter(value => value.startsWith("notice:")), ["notice:history.failedOpenSession"]);

  calls.length = 0;
  registration = deferred<string>();
  const waitingRemote = api.openRemoteProject({ hostId: "fixture", workspace: "waiting" }, { newSession: true });
  const winsRegistration = topic("wins-registration");
  registration.resolve("registered");
  assert.equal((await waitingRemote).status, "cancelled");
  assert.ok(!pending.has("remote:waiting"), "superseded registration cannot issue an OpenRemoteProjectTab request");
  await finish("wins-registration", winsRegistration);
  registration = undefined;

  calls.length = 0;
  const remote = api.openRemoteProject({ hostId: "fixture", workspace: "remote" }, { sessionName: "selected" });
  await act(async () => {});
  const remoteMeta = { ...tab("remote:remote"), remote: { hostId: "fixture", workspace: "remote" } };
  await act(async () => __emitMockRemoteTabOpened(remoteMeta));
  assert.deepEqual(calls.filter(value => /^(seed|remote-switch)/.test(value)), [], "opened event before the response cannot independently navigate");
  const localWins = topic("local-wins");
  pending.get("remote:remote")!.resolve(remoteMeta);
  assert.equal((await remote).status, "cancelled");
  await finish("local-wins", localWins);
  assert.ok(!calls.some(value => value.startsWith("remote-switch:")));
  calls.length = 0;
  const successfulRemote = api.openRemoteProject({ hostId: "fixture", workspace: "success" }, {});
  await act(async () => {});
  pending.get("remote:success")!.resolve({ ...remoteMeta, id: "remote:success" });
  const outcome = await successfulRemote;
  assert.equal(outcome.status, "completed");
  assert.ok(calls.includes(`remote-switch:remote:success:${intent}`), "the request's exact intent reaches dedicated remote activation");
  assert.ok(!calls.includes("tab-session"));

  calls.length = 0;
  const retainedRemote = api.openRemoteProject;
  const disposed = topic("disposed"), queued = topic("never");
  await act(async () => root.unmount());
  await finish("disposed", disposed); await queued;
  entry({ kind: "blank", scope: "global", workspaceRoot: "" });
  assert.deepEqual(calls.filter(value => /^(open|seed|tabs|notice|reveal|settle)/.test(value)), ["open:disposed"], "unmount releases pending input and fences queued and running continuations");
  assert.deepEqual(await retainedRemote({ hostId: "fixture", workspace: "disposed" }, {}), { status: "cancelled", reason: "disposed" });
  console.log("desktop navigation: queue ownership, ABA, IM hydrate, dirty-worktree warning, tab resume, failure and disposal passed");
} finally { dom.window.close(); }
