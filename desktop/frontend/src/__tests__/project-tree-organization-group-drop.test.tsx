// Run: tsx src/__tests__/project-tree-organization-group-drop.test.tsx

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { useProjectTreeOrganization } from "../components/ProjectTreeOrganization";
import type { ProjectNode, ProjectTreeOrganizationBindings, SessionGroup } from "../lib/types";
import type { SessionOrganizationMutation, SessionOrganizationSnapshot } from "../generated/desktopContract.generated";
import { ToastProvider } from "../lib/toast";
import { LocaleProvider } from "../lib/i18n";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1; else failed += 1;
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

function topic(id: string): ProjectNode {
  return { key: `topic-${id}`, kind: "topic", label: id, root: "/repo", topicId: id, session: { hostId: "local", sessionId: id } } as ProjectNode;
}

const inside = topic("inside");
const sibling = topic("sibling");
const outside = topic("outside");
const loose = topic("loose");
const folder: ProjectNode = { key: "project-/repo", kind: "project", label: "Repo", root: "/repo", children: [inside, sibling, outside, loose] };
const groups: SessionGroup[] = [{ id: "g1", title: "Group", sessionKeys: ["ref\0local\0inside", "ref\0local\0sibling"] }];

function Harness({ bindings }: { bindings: ProjectTreeOrganizationBindings }) {
  const organization = useProjectTreeOrganization({ tree: [folder], refresh: async () => {}, sortMode: "created", bindings });
  const loaded = organization.groupsFor(folder).length > 0;
  return <>
    <output id="loaded">{loaded ? "yes" : "no"}</output>
    {folder.children!.map((node) => {
      const row = organization.topicRow(node, false);
      return <div key={node.key} id={`row-${node.topicId}`} {...row.props}>{node.label}</div>;
    })}
  </>;
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function fireDrag(type: string, target: Element, data: Map<string, string>) {
  const event = new window.Event(type, { bubbles: true, cancelable: true });
  const dataTransfer = {
    setData: (format: string, value: string) => { data.set(format, value); },
    getData: (format: string) => data.get(format) ?? "",
    effectAllowed: "all",
    dropEffect: "none",
  };
  Object.defineProperty(event, "dataTransfer", { value: dataTransfer });
  Object.defineProperty(event, "clientY", { value: 10 });
  target.dispatchEvent(event);
}

async function dropOnto(draggedID: string, targetID: string): Promise<SessionOrganizationMutation[]> {
  const dom = installDom();
  const mutations: SessionOrganizationMutation[] = [];
  const snapshot = (): SessionOrganizationSnapshot => ({ groups: structuredClone(groups), revision: 1, applied: true, order: [], manualOrderEnabled: false });
  const bindings: ProjectTreeOrganizationBindings = {
    ReorderTopics: async () => {},
    ListProjectGroups: async () => [],
    SaveSessionGroups: async () => {},
    GetSessionOrganization: async () => snapshot(),
    UpdateSessionOrganization: async (_workspace, _revision, mutation) => { mutations.push(mutation); return snapshot(); },
  };
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<LocaleProvider><ToastProvider><Harness bindings={bindings} /></ToastProvider></LocaleProvider>);
    await flush();
  });
  for (let attempt = 0; attempt < 30 && document.getElementById("loaded")?.textContent !== "yes"; attempt += 1) await act(flush);
  const data = new Map<string, string>();
  await act(async () => { fireDrag("dragstart", document.getElementById(`row-${draggedID}`)!, data); await flush(); });
  await act(async () => { fireDrag("dragover", document.getElementById(`row-${targetID}`)!, data); await flush(); });
  await act(async () => { fireDrag("drop", document.getElementById(`row-${targetID}`)!, data); await flush(); });
  for (let attempt = 0; attempt < 10; attempt += 1) await act(flush);
  await act(async () => root.unmount());
  dom.window.close();
  return mutations;
}

console.log("\nproject tree drop onto a group member row");

{
  const mutations = await dropOnto("outside", "inside");
  ok(mutations.length === 1 && mutations[0].kind === "set-group" && mutations[0].groupId === "g1",
    `dropping a non-member on a member row adds it to that group (got ${JSON.stringify(mutations.map((m) => [m.kind, m.groupId ?? ""]))})`);
}

{
  const mutations = await dropOnto("sibling", "inside");
  ok(mutations.length === 1 && mutations[0].kind === "move",
    `dropping a member on another member of its group reorders (got ${JSON.stringify(mutations.map((m) => m.kind))})`);
}

{
  const mutations = await dropOnto("outside", "loose");
  ok(mutations.length === 1 && mutations[0].kind === "move",
    `dropping on an ungrouped row reorders (got ${JSON.stringify(mutations.map((m) => m.kind))})`);
}

{
  const mutations = await dropOnto("outside", "loose");
  ok(mutations.length === 1 && mutations[0].sortMode === "created",
    `a move carries the sort order on screen, so a first manual move starts from it (got ${JSON.stringify(mutations.map((m) => m.sortMode ?? ""))})`);
}

process.stdout.write(`\nproject-tree-organization-group-drop: ${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
