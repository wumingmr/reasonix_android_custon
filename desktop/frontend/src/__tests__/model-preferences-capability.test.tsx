import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import type { ModelSettingsChange, ProviderView } from "../lib/types";
import { selectSettingsValue, settingsOptionValues } from "./settingsSelectTestUtils";

const dom = new JSDOM("<html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
Object.assign(globalThis, {
  IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document,
  Node: dom.window.Node, HTMLElement: dom.window.HTMLElement, Event: dom.window.Event,
  CustomEvent: dom.window.CustomEvent, KeyboardEvent: dom.window.KeyboardEvent, MouseEvent: dom.window.MouseEvent,
  localStorage: dom.window.localStorage, sessionStorage: dom.window.sessionStorage,
  requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
  cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
});
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
window.scrollTo = () => {};
window.matchMedia = (() => ({ matches: true, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
Object.defineProperty(dom.window.HTMLElement.prototype, "getBoundingClientRect", { configurable: true, value: () => ({x:10,y:10,top:10,left:10,right:310,bottom:54,width:300,height:44,toJSON(){return {};}}) });
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { ModelsSection, ProviderEditor } = await import("../components/SettingsPanel");
const { baseSettings, installCanvasMock } = await import("../test-support/settingsTestFixtures");
const { installDesktopHostStub } = await import("./desktopHostStub");
installCanvasMock(dom.window as unknown as Window);
const changes: Array<{ field?: string; ref?: string }> = [];
installDesktopHostStub({ ApplyModelSettings: async (change: ModelSettingsChange) => {
  changes.push(change);
  return {requestId:change.requestId, persisted:true, revision:"saved", application:"not_required", targets:[], issues:[], appliedCatalogs:[]};
} });

const provider: ProviderView = {
  name: "gateway", kind: "openai", baseUrl: "https://gateway.invalid/v1",
  models: ["reasoner", "text-model"], default: "text-model", added: true, builtIn: false,
  modelsUrl: "", apiKeyEnv: "", keySet: false, requiresKey: false, configured: true,
  visionModels: [], balanceUrl: "", contextWindow: 0, reasoningProtocol: "", thinking: "",
  supportedEfforts: [], defaultEffort: "low",
  modelOverrides: [{model:"text-model", reasoningProtocol:"none", supportedEfforts:[], defaultEffort:""}],
  modelCapabilities: [
    {model:"reasoner", inputModalities:[], state:"unknown", source:"unknown",
      reasoning:{state:"supported", options:[{id:"low",name:"low"},{id:"high",name:"high"}]}},
    {model:"text-model", inputModalities:[], state:"unsupported", source:"override",
      reasoning:{state:"unsupported", options:[]}},
  ],
};
const host = document.getElementById("root")!;
const root = createRoot(host);
const settings = baseSettings();
settings.providers = [provider];
settings.defaultModel = "gateway/reasoner";
settings.plannerModel = settings.defaultModel;
settings.subagentModel = settings.defaultModel;
const render = async () => act(async () => {
  root.render(<LocaleProvider><ModelsSection s={{...settings}} busy={false} subtab="usage"
    apply={async fn => { await fn(); return true; }} backgroundApply={async () => true} /></LocaleProvider>);
});
const effortControl = () => host.querySelector<HTMLButtonElement>('button[aria-label="Subagent reasoning effort"]')!;
await render();
const planner = host.querySelector<HTMLButtonElement>('button[aria-label="Dedicated planner model"]')!;
assert.ok(planner, "planner control exists");
assert.equal(planner.textContent, "reasoner", "explicit planner remains visible even when equal to default");
await selectSettingsValue(planner, "");
assert.equal(changes.at(-1)?.field, "planner");
assert.equal(changes.at(-1)?.ref, "", "follow clears the explicit planner");
assert.deepEqual(await settingsOptionValues(effortControl()), ["", "low", "high"], "resolved options work without legacy levels");
await selectSettingsValue(effortControl(), "high");
assert.equal(changes.at(-1)?.field, "subagent_effort");
assert.equal(changes.at(-1)?.ref, "high");
settings.subagentModel = "";
await render();
assert.deepEqual(await settingsOptionValues(effortControl()), ["", "low", "high"], "follow-parent selection uses the backend's default-model validation target");
settings.subagentModel = "gateway/text-model";
provider.supportedEfforts = ["low", "high"];
await render();
assert.deepEqual(await settingsOptionValues(effortControl()), [""], "unsupported model does not inherit stale connection levels");
settings.subagentEffort = "high";
await render();
await act(async () => effortControl().click());
assert.equal(document.querySelector('[role="option"][data-value="high"]')?.getAttribute("aria-disabled"), "true", "stale selection is visible but unavailable");
await act(async () => effortControl().click());
await selectSettingsValue(effortControl(), "");
assert.equal(changes.at(-1)?.ref, "", "stale effort can be cleared");
settings.subagentEffort = "";
settings.subagentModel = "gateway/reasoner";
provider.modelCapabilities![0].reasoning = {state:"unknown", options:[]};
await render();
assert.deepEqual(await settingsOptionValues(effortControl()), [""], "unknown resolved capability does not fall back to stale levels");
settings.subagentModel = "gateway/text-model";
provider.modelCapabilities = [];
await render();
assert.deepEqual(await settingsOptionValues(effortControl()), [""], "legacy per-model reasoning=none still disables levels");
settings.subagentModel = "gateway/reasoner";
await render();
assert.deepEqual(await settingsOptionValues(effortControl()), ["", "low", "high"], "older hosts retain declared levels");

async function saveRename(initial: ProviderView, disableReasoning = false): Promise<ProviderView> {
  let saved: ProviderView | undefined;
  await act(async () => root.render(<LocaleProvider><ProviderEditor key={initial.name} initial={initial} kinds={["openai"]} busy={false}
    onCancel={() => {}} onSave={value => { saved = value; }} /></LocaleProvider>));
  const name = host.querySelector<HTMLInputElement>(".provider-name-input")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(name, "Renamed only");
    name.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
  if (disableReasoning) {
    await selectSettingsValue(host.querySelector<HTMLButtonElement>('button[aria-label="Model capability mode"]')!, "none");
  }
  const save = Array.from(host.querySelectorAll<HTMLButtonElement>("button")).find(el => el.textContent?.trim() === "Save changes")!;
  assert.ok(save && !save.disabled);
  await act(async () => save.click());
  assert.ok(saved);
  return saved;
}
const saved = await saveRename({...provider, name:"preserve", supportedEfforts:[]});
assert.equal(saved.default, "text-model", "rename preserves non-first default model");
assert.equal(saved.defaultEffort, "low", "rename preserves default with inferred levels");
assert.deepEqual(saved.supportedEfforts, [], "automatic options are not frozen into config");
const removed = await saveRename({...provider, name:"removed", models:["reasoner"]});
assert.equal(removed.default, "reasoner", "removed default falls back to an enabled model");
const invalid = await saveRename({...provider, name:"diagnostic", defaultEffort:"future-level"});
assert.equal(invalid.defaultEffort, "future-level", "unrelated edit preserves an invalid declaration for backend diagnostics");
const disabled = await saveRename({...provider, name:"explicit-off"}, true);
assert.equal(disabled.defaultEffort, "", "explicitly disabling reasoning clears its default");
assert.deepEqual(disabled.supportedEfforts, []);
await act(async () => root.unmount());
dom.window.close();
console.log("model preferences capability and preservation regressions passed");
