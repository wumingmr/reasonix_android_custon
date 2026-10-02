import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { AddProviderPanel } from "../components/SettingsPanel";
import { LocaleProvider } from "../lib/i18n";
import type { ProviderView } from "../lib/types";
import { mockProviderTemplate } from "../lib/mockProviderTemplates";

const legacy: ProviderView = {
  ...mockProviderTemplate({
    name: "deepseek", kind: "openai",
    baseUrl: "https://api.deepseek.com", apiKeyEnv: "DEEPSEEK_API_KEY",
    models: ["deepseek-v4-flash", "custom-ID"], default: "custom-ID",
  }),
  builtIn: true,
};
for (const existing of [[], [legacy]]) {
  const html = renderToStaticMarkup(<LocaleProvider><AddProviderPanel
    mode="official" kinds={["openai", "anthropic", "responses"]}
    officialProviders={existing} providerPresets={[]} busy={false}
    onMode={() => {}} onCancel={() => {}} onAddOfficial={async () => {}}
    onAddPreset={async () => {}} onViewPresetConflict={() => {}}
    onResetPreset={async () => {}} onAddCustom={() => {}}
  /></LocaleProvider>);
  assert.match(html, /deepseek-flash, deepseek-v4-pro/);
  assert.doesNotMatch(html, /deepseek-v4-flash|custom-ID/);
}
assert.deepEqual(legacy.models, ["deepseek-v4-flash", "custom-ID"]);
assert.equal(legacy.default, "custom-ID");
console.log("PASS official provider catalog: current defaults with legacy connections preserved");
