// Run: pnpm exec tsx src/__tests__/interrupted-turn-provider-error.test.ts
import assert from "node:assert/strict";

import { initialState, reducer } from "../lib/useController";

function finish(e: Record<string, unknown>) {
  const started = reducer(initialState, { type: "event", e: { kind: "turn_started" } });
  return reducer(started, { type: "event", e: { kind: "turn_done", ...e } });
}

const quota = finish({
  status: "recovery_required",
  err: "relay · Chat Completions: provider balance or plan quota insufficient (HTTP 402)",
  detail: "Connection ID: relay",
  diagnostic: { kind: "quota", status: 402, protocol: "openai" },
  recovery: { state: "recovery_required", reason: "silent_interruption" },
});
const notices = quota.items.filter((item) => item.kind === "notice");
const failure = notices.find((item) => item.kind === "notice" && item.level === "warn");
assert.ok(failure?.kind === "notice" && failure.text.includes("HTTP 402"), "provider error stays visible on a recovery_required turn");
assert.equal(failure?.kind === "notice" ? failure.detail : "", "Connection ID: relay");
assert.ok(notices.some((item) => item.kind === "notice" && item.level === "info"), "interrupted guidance stays alongside the error");
assert.equal(new Set(quota.items.map((item) => item.id)).size, quota.items.length, "notice ids stay unique");

const cancelled = finish({ status: "interrupted", err: "context canceled", diagnostic: { kind: "cancelled" } });
assert.ok(!cancelled.items.some((item) => item.kind === "notice" && item.level === "warn"), "a user stop is not reported as a provider failure");

console.log("interrupted-turn-provider-error: ok");
