// Run: pnpm exec tsx src/__tests__/project-tree-organization-reset.test.tsx
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { useProjectTreeOrganization } from "../components/ProjectTreeOrganization";
import type { ProjectNode, ProjectTreeOrganizationBindings } from "../lib/types";
import type { SessionOrganizationMutation, SessionOrganizationSnapshot } from "../generated/desktopContract.generated";
import { ToastProvider } from "../lib/toast";
import { LocaleProvider, t } from "../lib/i18n";

const dom = new JSDOM('<html><body><div id="root"></div></body></html>', { url: "http://localhost/" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, Node: dom.window.Node, Element: dom.window.Element,
  HTMLElement: dom.window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });

const folder: ProjectNode = { key: "project-/repo", kind: "project", label: "Repo", root: "/repo", children: [] };
const initialOrder = ["ref\0local\0older", "ref\0local\0newer"];
let snapshot: SessionOrganizationSnapshot = { revision: 1, applied: true, manualOrderEnabled: true, order: initialOrder, groups: [] };
const mutations: SessionOrganizationMutation[] = [];
let refreshes = 0;
const bindings: ProjectTreeOrganizationBindings = {
  GetSessionOrganization: async () => snapshot,
  UpdateSessionOrganization: async (_workspace, revision, mutation) => {
    assert.equal(revision, snapshot.revision);
    mutations.push(mutation);
    snapshot = { ...snapshot, revision: revision + 1, manualOrderEnabled: false };
    return snapshot;
  },
};

function Harness() {
  const organization = useProjectTreeOrganization({ tree: [folder], refresh: async () => { refreshes += 1; }, bindings });
  const resetItem = organization.resetOrderMenuItems(folder, t, () => {});
  return <><output id="order">{organization.orderFor?.(folder).join(",")}</output>
    {resetItem.map(item => <button key={item.key} id="reset" onClick={item.onSelect}>Reset</button>)}</>;
}

const root = createRoot(document.getElementById("root")!);
const flush = async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); }); };
try {
  await act(async () => root.render(<LocaleProvider><ToastProvider><Harness /></ToastProvider></LocaleProvider>));
  await flush();
  assert.equal(document.getElementById("order")?.textContent, initialOrder.join(","));
  await act(async () => { document.getElementById("reset")!.click(); });
  await flush();
  assert.deepEqual(mutations.map(mutation => mutation.kind), ["reset-order"]);
  assert.equal(document.getElementById("order")?.textContent, "");
  assert.equal(refreshes, 1);
} finally {
  await act(async () => root.unmount());
  dom.window.close();
}
