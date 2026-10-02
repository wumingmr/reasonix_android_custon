// Run: tsx src/__tests__/project-tree-organization-races.test.tsx

import { JSDOM } from "jsdom";
import React, { StrictMode } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useProjectTreeOrganization } from "../components/ProjectTreeOrganization";
import type { ProjectNode, ProjectTreeOrganizationBindings, SessionGroup } from "../lib/types";
import type { SessionOrganizationSnapshot } from "../generated/desktopContract.generated";
import { ToastProvider } from "../lib/toast";
import { LocaleProvider } from "../lib/i18n";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1; else failed += 1;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function installDom() {
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  globalThis.Node = dom.window.Node;
  globalThis.Element = dom.window.Element;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.Event = dom.window.Event;
  globalThis.MouseEvent = dom.window.MouseEvent;
  return dom;
}

const folder: ProjectNode = {
  key: "project-/repo",
  kind: "project",
  label: "Repo",
  root: "/repo",
  children: [],
};

function Harness({ bindings, revision = 0 }: { bindings: ProjectTreeOrganizationBindings; revision?: number }) {
  const organization = useProjectTreeOrganization({
    tree: [folder],
    refresh: async () => {},
    organizationRevision: revision,
    bindings,
  });
  const groups = organization.groupsFor(folder);
  return <>
    <output id="groups">{JSON.stringify(groups)}</output>
    <button id="create" onClick={() => organization.createGroup(folder, "Local")}>create</button>
  </>;
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    await act(flush);
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function mount(bindings: ProjectTreeOrganizationBindings) {
  const dom = installDom();
  const root = createRoot(document.getElementById("root")!);
  let revision = 0;
  const render = async (nextRevision = revision) => {
    revision = nextRevision;
    await act(async () => {
      root.render(<StrictMode><LocaleProvider><ToastProvider><Harness bindings={bindings} revision={revision} /></ToastProvider></LocaleProvider></StrictMode>);
      await flush();
    });
  };
  await render();
  return { dom, root, render };
}

async function cleanup(dom: JSDOM, root: Root) {
  await act(async () => root.unmount());
  dom.window.close();
}

function legacyBindings(list: () => Promise<SessionGroup[]>): ProjectTreeOrganizationBindings {
  return {
    ReorderTopics: async () => {},
    ListProjectGroups: async () => list(),
    SaveSessionGroups: async () => {},
  };
}
function snapshot(groups: SessionGroup[], revision = 1, applied = true): SessionOrganizationSnapshot {
  return { groups: structuredClone(groups), revision, applied, order: [], manualOrderEnabled: false };
}
const rejectLegacyWrite = async () => { throw new Error("new UI must never invoke an unversioned or topic group writer"); };
function organizationBindings(read: () => Promise<SessionOrganizationSnapshot>, write: NonNullable<ProjectTreeOrganizationBindings["UpdateSessionOrganization"]>): ProjectTreeOrganizationBindings {
  return { ReorderTopics: rejectLegacyWrite, ListProjectGroups: async () => { throw new Error("new API must own reads"); }, SaveSessionGroups: rejectLegacyWrite,
    GetSessionOrganization: read, UpdateSessionOrganization: write };
}

console.log("\nproject tree organization races");

{
  const initial = deferred<SessionOrganizationSnapshot>();
  const { dom, root } = await mount(organizationBindings(() => initial.promise, async () => { throw new Error("read only scenario"); }));
  await act(async () => {
    initial.resolve(snapshot([{ id: "existing", title: "Existing", sessionKeys: [] }]));
    await flush();
  });
  await waitFor("StrictMode group load", () => document.getElementById("groups")?.textContent?.includes("Existing") === true);
  ok(true, "StrictMode effect replay does not discard the deferred group load");
  await cleanup(dom, root);
}

{
  const initial = deferred<SessionOrganizationSnapshot>();
  const write = deferred<void>();
  let reads = 0;
  const { dom, root } = await mount(organizationBindings(() => ++reads === 1 ? initial.promise : Promise.resolve(snapshot([])), async (_workspace, expected, mutation) => {
    await write.promise;
    return snapshot([{ id: mutation.groupId!, title: mutation.title!, sessionKeys: [] }], expected + 1);
  }));
  await act(async () => {
    (document.getElementById("create") as HTMLButtonElement).click();
    await flush();
  });
  initial.resolve(snapshot([{ id: "stale", title: "Stale", sessionKeys: [] }]));
  await act(flush);
  const text = document.getElementById("groups")?.textContent ?? "";
  ok(text.includes("Local") && !text.includes("Stale"), "a stale initial read cannot overwrite an optimistic mutation");
  await act(async () => { write.resolve(); await flush(); });
  await cleanup(dom, root);
}

{
  let state: SessionGroup[] = [];
  let revision = 0;
  let conflictInjected = false;
  const writes: number[] = [];
  const bindings = organizationBindings(async () => snapshot(state, revision), async (_workspace, expected, mutation) => {
      writes.push(expected);
      if (!conflictInjected) {
        conflictInjected = true;
        state = [{ id: "remote", title: "Remote", sessionKeys: [] }];
        revision += 1;
      }
      if (expected !== revision) return snapshot(state, revision, false);
      state.push({ id: mutation.groupId!, title: mutation.title!, sessionKeys: [] });
      revision += 1;
      return snapshot(state, revision);
    });
  const { dom, root } = await mount(bindings);
  await act(async () => {
    (document.getElementById("create") as HTMLButtonElement).click();
    await flush();
  });
  await waitFor("CAS rebase", () => state.length === 2);
  await waitFor("CAS UI reconciliation", () => {
    const text = document.getElementById("groups")?.textContent ?? "";
    return text.includes("Remote") && text.includes("Local");
  });
  ok(state.some((group) => group.title === "Remote") && state.some((group) => group.title === "Local"),
    "CAS conflict rebases and displays the local mutation without losing the remote group");
  ok(JSON.stringify(writes) === "[0,1]", "conflict replays one semantic mutation against the returned revision");
  await cleanup(dom, root);
}

{
  let state: SessionGroup[] = [{ id: "one", title: "One", sessionKeys: ["ref\x00local\x00archived"] }];
  let revision = 1;
  const bindings = organizationBindings(async () => snapshot(state, revision), async () => snapshot(state, revision, false));
  const { dom, root, render } = await mount(bindings);
  await waitFor("initial archive membership", () => document.getElementById("groups")?.textContent?.includes("archived") === true);
  state = [{ id: "one", title: "One", sessionKeys: [] }];
  revision += 1;
  await render(1);
  await waitFor("metadata invalidation", () => !document.getElementById("groups")?.textContent?.includes("archived"));
  ok(true, "metadata revision invalidates loaded groups after archive cleanup");
  await cleanup(dom, root);
}

{
  let legacyWrites = 0;
  const bindings = legacyBindings(async () => [{ id: "existing", title: "Authoritative", sessionKeys: [] }]);
  bindings.SaveSessionGroups = async () => { legacyWrites++; };
  const { dom, root } = await mount(bindings);
  await waitFor("legacy read", () => document.getElementById("groups")?.textContent?.includes("Authoritative") === true);
  await act(async () => { (document.getElementById("create") as HTMLButtonElement).click(); await flush(); });
  await waitFor("unsupported write restores authority", () => !document.getElementById("groups")?.textContent?.includes("Local"));
  ok(legacyWrites === 0 && document.getElementById("groups")?.textContent?.includes("Authoritative") === true,
    "missing versioned API never falls back to old writes and restores authoritative groups");
  ok(document.querySelector(".toast--error")?.textContent?.includes("Upgrade the desktop service") === true,
    "unsupported writer produces a visible upgrade error");
  await cleanup(dom, root);
}

process.stdout.write(`\nproject-tree-organization-races: ${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
