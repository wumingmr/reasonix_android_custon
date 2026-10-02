// A completed turn forks from its persisted boundary, identified by the
// assistant message it ends with. Index, page offset, and checkpoint state must
// not move the cut, and every refusal keeps its own reason.
import assert from "node:assert/strict";
import { act } from "react";
import { createTranscriptHarness } from "./transcript-dom-harness";
import type { Item } from "../lib/useController";
import type { ForkTargetSetView } from "../lib/forkTargets";
const harness = await createTranscriptHarness();
const items: Item[] = [
  { kind: "user", id: "u42", text: "history starts mid-session", checkpointTurn: 42 },
  { kind: "assistant", id: "m:a42", text: "answer", reasoning: "", streaming: false,
    createdAt: new Date(2026, 8, 12, 12, 34).getTime(), turnDurationMs: 29_000, tokensPerSecond: 183,
    turnUsage: { totalTokens: 185_225, uncachedInputTokens: 26_278, cacheReadTokens: 155_520, outputTokens: 3_427, reasoningTokens: 1_909, routes: ["deepseek-official/deepseek-flash"] } },
];
const target = (targets: ForkTargetSetView["targets"], verifiable = true): ForkTargetSetView => ({ targets, verifiable });
const available = { sourceSessionId: "source-1", sessionGeneration: 4, turnId: "turn-42", boundarySequence: 99, turnNumber: 42, status: "committed", messageId: "a42", available: true };
const calls: typeof available[] = [];
const props = { forkTargets: target([available]), onFork: (forkTarget: typeof available) => { calls.push(forkTarget); } };
try {
  await harness.render(items, props); await harness.settle();
  const branch = () => harness.container.querySelector<HTMLButtonElement>(".chat-actions button.chat-action-icon:not(.copybtn)")!;
  assert.ok(branch(), "completed turn exposes the icon-only Harness branch action");
  assert.equal(branch().textContent, "", "branch label stays in the tooltip instead of the reading flow");
  assert.equal(branch().closest(".chat-actions")?.getAttribute("data-actions-reveal"), "always", "latest turn actions remain visible");
  assert.equal(branch().hasAttribute("disabled"), false, "available branch is interactive");
  assert.equal(branch().getAttribute("aria-disabled"), null);
  await act(async () => { branch().focus(); await new Promise(resolve => setTimeout(resolve, 1)); });
  assert.equal(harness.dom.window.document.querySelector('[role="tooltip"]')?.textContent, branch().getAttribute("aria-label"), "keyboard focus exposes the Harness tooltip");
  await act(async () => branch().click());
  assert.deepEqual(calls, [available], "an enabled branch carries the complete source and boundary anchor");
  const statButtons = () => [...harness.container.querySelectorAll<HTMLButtonElement>(".chat-stat-trigger")];
  assert.equal(statButtons().length, 2, "completed answers expose Harness usage and time pills");
  assert.match(statButtons()[0].textContent ?? "", /185\.2K|185K/);
  await act(async () => statButtons()[0].click());
  const usageDialog = harness.dom.window.document.querySelector<HTMLElement>("[data-turn-usage-details]")!;
  assert.ok(usageDialog, "usage pill opens its anchored details dialog");
  assert.match(usageDialog.textContent ?? "", /deepseek-official\/deepseek-flash/);
  assert.match(usageDialog.textContent ?? "", /155,520/);
  await act(async () => statButtons()[1].click());
  const timeDialog = harness.dom.window.document.querySelector<HTMLElement>("[data-turn-time-details]")!;
  assert.ok(timeDialog, "time pill opens its anchored details dialog");
  assert.match(timeDialog.textContent ?? "", /29/);
  assert.match(timeDialog.textContent ?? "", /183/);
  assert.match(harness.container.querySelector(".chat-actions__time")?.textContent ?? "", /12:34/);
  assert.equal(harness.container.textContent?.includes("Like"), false, "feedback actions are intentionally not transplanted");
  assert.equal(harness.container.querySelector(".msg-edit"), null);

  const refusal = async (overrides: Record<string, unknown>, expect: RegExp, label: string) => {
    await harness.render(items, { ...props, ...overrides }); await harness.settle();
    const before = calls.length;
    assert.equal(branch().hasAttribute("disabled"), false, `${label}: unavailable action remains focusable for its explanation`);
    assert.equal(branch().getAttribute("aria-disabled"), "true", `${label}: reports itself unavailable`);
    assert.equal(branch().getAttribute("aria-describedby"), null, `${label}: needs no remount-sensitive description node`);
    assert.match(branch().getAttribute("aria-label") ?? "", expect, `${label}: includes its reason in the accessible name`);
    await act(async () => branch().click());
    assert.equal(calls.length, before, `${label}: never dispatches`);
  };
  await refusal({ forkTargets: undefined }, /Checking which turns/, "unloaded set");
  await refusal({ forkTargets: { targets: [], verifiable: false } }, /verifiable branch boundary/, "legacy history");
  await refusal({ forkTargets: target([{ ...available, available: false, reason: "turn_open" }]) }, /not finished yet/, "open turn");
  // The boundary is proven here; it is the child it would carry that is unsafe,
  // so this refusal must not read as a missing boundary.
  await refusal({ forkTargets: target([{ ...available, available: false, reason: "active_authority" }]) }, /question or approval.*tool action.*still open/, "unusable boundary");
  await refusal({ forkTargets: target([{ ...available, turnId: "turn-41", messageId: "a41" }]) }, /not finished yet/, "answer without a persisted boundary");
  await refusal({ forkBlocked: "creating" }, /Creating the branch/, "request in flight");
  await refusal({ forkBlocked: "read_only" }, /does not allow creating/, "read-only surface");
  await refusal({ forkBlocked: "unsupported" }, /server's version/, "server without create-only fork");
  for (const running of [{ running: true }, { hydrating: true }]) {
    calls.length = 0;
    await harness.render(items, { ...props, ...running }); await harness.settle();
    await act(async () => branch().click());
    assert.deepEqual(calls, [available], "the fork entry follows the persisted boundary, not the turn's runtime state");
  }
  console.log("chat branches: message identity, per-state reasons and persisted boundaries passed");
} finally { await harness.unmount(); await harness.close(); }
