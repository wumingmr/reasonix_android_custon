import assert from "node:assert/strict";
import { mock } from "node:test";
import { register } from "node:module";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { installRemoteSurfaceDom } from "./helpers/remoteSurfaceDom";
import { LocaleProvider } from "../lib/i18n";
import type { LoadingFixtureInput } from "../test-support/sessionLoadingFixture";

register(new URL("../../scripts/svg-loader.mjs", import.meta.url));
register(new URL("../../scripts/css-loader.mjs", import.meta.url));
const dom = installRemoteSurfaceDom();
globalThis.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
const { SessionLoadingFixture } = await import("../test-support/sessionLoadingFixture");
const root = createRoot(document.getElementById("root")!);
const paint = (input: LoadingFixtureInput) => act(async () => root.render(
  <LocaleProvider><SessionLoadingFixture {...input} /></LocaleProvider>,
));
const tick = (ms: number) => act(async () => mock.timers.tick(ms));
const indicator = () => document.querySelector(".session-loading-indicator");
const noRecovery = () => assert.equal(document.querySelector(".session-recovery, .session-recovery-placeholder"), null,
  "normal loading never presents recovery or retry language");
mock.timers.enable({ apis: ["setTimeout"] });
try {
  for (const surface of ["local", "remote", "transcript"] as const) {
    const input: LoadingFixtureInput = { surface, identity: `${surface}-A`, phase: "loading" };
    await paint(input);
    await tick(249);
    assert.equal(indicator(), null, `${surface}: quick loads have no visible loading feedback`);
    noRecovery();
    await paint({ ...input, phase: "ready" });
    assert.match(document.body.textContent!, new RegExp(`Content ${input.identity}`), "ready content appears without a minimum wait");
    await tick(2500);
    assert.equal(indicator(), null, "completed timers cannot resurrect the indicator");

    await paint(input);
    await tick(250);
    assert.equal(document.querySelectorAll(".session-loading-indicator").length, 1, "one loading owner across surfaces");
    const shortLabel = indicator()!.textContent;
    await tick(1750);
    assert.notEqual(indicator()!.textContent, shortLabel, "slow load adds a specific explanation after two seconds");
    noRecovery();
    await paint({ ...input, identity: `${surface}-B` });
    assert.equal(indicator(), null, "a new session never inherits the old loading stage");
    await tick(200);
    await paint(input);
    await tick(50);
    assert.equal(indicator(), null, "rapid A/B/A switching cancels B's deadline");
    await tick(200);
    assert.ok(indicator(), "the latest A owns its own deadline");
    await paint({ ...input, phase: "ready" });
    assert.equal(indicator(), null, "ready content removes an already visible indicator immediately");

    await paint({ ...input, cached: true });
    const cachedNode = document.querySelector(".chat-node");
    assert.ok(cachedNode, "cached target content is mounted while history refreshes");
    await tick(2000);
    assert.equal(document.querySelector(".chat-node"), cachedNode, "loading stages do not replace cached content");
    assert.equal(document.querySelectorAll(".session-loading-indicator").length, 1);
    noRecovery();
    await paint({ ...input, phase: "ready" });
    assert.equal(document.querySelector(".chat-node"), cachedNode, "completion preserves the cached node");
    await paint(input);
    await tick(2000);
    await paint({ ...input, generation: 2 });
    assert.equal(indicator(), null, "same-tab session replacement resets feedback before paint");
    await tick(249);
    assert.equal(indicator(), null, "replacement generation owns a fresh deadline");
    await paint({ ...input, generation: 2, phase: "ready" });
  }
  for (const surface of ["local", "remote"] as const) {
    let retries = 0;
    const input: LoadingFixtureInput = { surface, identity: `${surface}-error`, phase: "loading", onRetry: async () => { retries++; } };
    await paint(input);
    await tick(100);
    await paint({ ...input, phase: "error" });
    assert.ok(document.querySelector('[role="alert"]'), "real errors are immediate, even inside the loading grace period");
    assert.equal(indicator(), null);
    await act(async () => document.querySelector<HTMLButtonElement>(".session-recovery .btn--primary")!.click());
    assert.equal(retries, 1, "error offers the owning retry action");
    await paint(input);
    noRecovery();
    await tick(249);
    assert.equal(indicator(), null, "retry gets a fresh quiet period");
    await paint({ ...input, phase: "ready" });
    await tick(2500);
    assert.equal(indicator(), null);
  }
  await paint({ surface: "local", identity: "readable-runtime", phase: "loading", cached: true, source: "runtime" });
  await tick(2000);
  assert.equal(indicator(), null, "runtime activation does not obscure already readable history");
  assert.match(document.body.textContent!, /Content readable-runtime/);
  await paint({ surface: "remote", identity: "connecting", phase: "loading", source: "connection" });
  await tick(2000);
  assert.ok(indicator());
  noRecovery();
  await act(async () => root.unmount());
  await tick(3000);
  console.log("PASS session loading: quiet fast loads, staged slow loads, cache continuity, retry, and identity-fenced timers on local/remote/standalone surfaces");
} finally {
  mock.timers.reset();
  dom.window.close();
}
