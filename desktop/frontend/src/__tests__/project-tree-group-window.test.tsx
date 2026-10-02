// Run: npx tsx src/__tests__/project-tree-group-window.test.tsx
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act, useState } from "react";
import { createRoot } from "react-dom/client";
import type { Translator } from "../lib/i18n";
import type { ProjectNode, SessionGroup } from "../lib/types";
import type { ProjectTreeOrganizationController } from "../components/ProjectTreeOrganization";

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { url: "http://localhost/" });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  HTMLElement: dom.window.HTMLElement,
  Element: dom.window.Element,
  requestAnimationFrame: (callback: FrameRequestCallback) => setTimeout(() => callback(Date.now()), 0),
  cancelAnimationFrame: (id: number) => clearTimeout(id),
  IS_REACT_ACT_ENVIRONMENT: true,
});
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });

const { ProjectTreeGroupRows } = await import("../components/ProjectTreeOrganization");

const folder: ProjectNode = { key: "project", kind: "project", label: "Project", root: "/project", children: [] };
const groups: SessionGroup[] = [
  { id: "feature", title: "Feature", topicIds: Array.from({ length: 16 }, (_, index) => `feature-${index}`) },
  { id: "bugs", title: "Bugs", topicIds: Array.from({ length: 16 }, (_, index) => `bugs-${index}`) },
];
const topic = (id: string): ProjectNode => ({ key: `topic-${id}`, kind: "topic", label: id, topicId: id, children: [] });
const children = [
  ...Array.from({ length: 16 }, (_, index) => topic(`plain-${index}`)),
  ...groups.flatMap((group) => (group.topicIds ?? []).map(topic)),
];

const organization: ProjectTreeOrganizationController = {
  topicRow: () => ({ className: "", props: {} }),
  topicMenuItems: () => [],
  createGroup: () => {},
  groupsFor: () => groups,
  groupCollapsed: () => false,
  toggleGroup: () => {},
  renameGroup: () => {},
  deleteGroup: () => {},
  canDropTopicInto: () => false,
  dropTopicInto: () => {},
};

const t = ((key: string, values?: Record<string, string>) => {
  if (key === "projectTree.expandDisplay") return "Show more";
  if (key === "projectTree.expandGroup") return `Show more in ${values?.name}`;
  return key;
}) as Translator;

function Harness() {
  const [limits, setLimits] = useState<Record<string, number>>({ "": 5, feature: 5, bugs: 5 });
  return <ProjectTreeGroupRows
    folder={folder}
    children={children}
    depth={1}
    section="projects"
    visible
    organization={organization}
    renderNode={(node) => <div data-topic={node.topicId} key={node.key} />}
    t={t}
    queryActive={false}
    remote={false}
    activeTopicId={undefined}
    isActive={() => false}
    listState={(groupID) => ({ itemKeys: (groupID ? groups.find((group) => group.id === groupID)?.topicIds : children.filter((node) => node.topicId?.startsWith("plain-")).map((node) => node.key))?.map((id) => id.startsWith("topic-") ? id : `topic-${id}`) ?? [], loading: false, initialized: true })}
    listLimit={(groupID) => limits[groupID] ?? 5}
    onEnsureList={() => {}}
    onExpandList={(groupID) => setLimits((current) => ({ ...current, [groupID]: (current[groupID] ?? 5) + 5 }))}
    onRetryList={() => {}}
    onForgetList={() => {}}
  />;
}

const container = document.getElementById("root")!;
const root = createRoot(container);
await act(async () => root.render(<Harness />));
const rows = (prefix: string) => container.querySelectorAll(`[data-topic^="${prefix}"]`).length;
assert.equal(rows("plain-"), 5);
assert.equal(rows("feature-"), 5);
assert.equal(rows("bugs-"), 5);

await act(async () => (container.querySelector('[aria-label="Show more in Feature"]') as HTMLButtonElement).click());
assert.equal(rows("feature-"), 10);
assert.equal(rows("bugs-"), 5, "expanding one group leaves the other group unchanged");
assert.equal(rows("plain-"), 5, "expanding a group leaves ungrouped rows unchanged");
assert.equal(container.querySelectorAll('[aria-label^="Show less in "]').length, 0, "session windows expose no competing collapse action");

await act(async () => (container.querySelector('[aria-label="Show more in Feature"]') as HTMLButtonElement).click());
assert.equal(rows("feature-"), 15);
await act(async () => (container.querySelector('[aria-label="Show more in Feature"]') as HTMLButtonElement).click());
assert.equal(rows("feature-"), 16);
assert.equal(container.querySelector('[aria-label="Show more in Feature"]'), null, "the final loaded row removes the one-way disclosure");

await act(async () => root.unmount());

let openedActiveGroup = "";
const collapsedOrganization: ProjectTreeOrganizationController = {
  ...organization,
  groupCollapsed: (_key, groupID) => groupID === "feature",
  toggleGroup: (_key, groupID) => { openedActiveGroup = groupID; },
};
const secondRoot = createRoot(container);
await act(async () => secondRoot.render(<ProjectTreeGroupRows
  folder={folder}
  children={children}
  depth={1}
  section="projects"
  visible
  organization={collapsedOrganization}
  renderNode={(node) => <div data-topic={node.topicId} key={node.key} />}
  t={t}
  queryActive={false}
  remote={false}
  activeTopicId="feature-10"
  isActive={(node) => node.topicId === "feature-10"}
  listState={(groupID) => ({ itemKeys: groupID === "feature" ? groups[0].topicIds?.map((id) => `topic-${id}`) : [], loading: false, initialized: true })}
  listLimit={() => 5}
  onEnsureList={() => {}}
  onExpandList={() => {}}
  onRetryList={() => {}}
  onForgetList={() => {}}
/>));
assert.equal(openedActiveGroup, "feature", "initial active navigation opens its collapsed group once");
await act(async () => secondRoot.unmount());

const ensuredGroups: string[] = [];
function LazyGroupHarness() {
  const [featureCollapsed, setFeatureCollapsed] = useState(true);
  const lazyOrganization: ProjectTreeOrganizationController = {
    ...organization,
    groupCollapsed: (_key, groupID) => featureCollapsed && groupID === "feature",
    toggleGroup: (_key, groupID) => { if (groupID === "feature") setFeatureCollapsed((value) => !value); },
  };
  return <ProjectTreeGroupRows
    folder={folder}
    children={children}
    depth={1}
    section="projects"
    visible
    organization={lazyOrganization}
    renderNode={(node) => <div data-topic={node.topicId} key={node.key} />}
    t={t}
    queryActive={false}
    remote={false}
    activeTopicId={undefined}
    isActive={() => false}
    listState={() => ({ loading: false, initialized: false })}
    listLimit={() => 5}
    onEnsureList={(groupID) => { ensuredGroups.push(groupID); }}
    onExpandList={() => {}}
    onRetryList={() => {}}
    onForgetList={() => {}}
  />;
}
const thirdRoot = createRoot(container);
await act(async () => thirdRoot.render(<LazyGroupHarness />));
assert.deepEqual(ensuredGroups, ["", "bugs"], "collapsed groups defer their first page until they are opened");
await act(async () => (container.querySelector(".project-tree__group-main") as HTMLElement).click());
assert.ok(ensuredGroups.includes("feature"), "opening a collapsed group requests its first page");
await act(async () => thirdRoot.unmount());
console.log("  PASS  project tree group windows are independent");
