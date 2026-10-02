// Run: tsx src/__tests__/hydrate-failure-attribution.test.ts
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import type { FollowRequest, TranscriptFollowResponse } from "../generated/desktopContract.generated";
import { hydrateFailureDetail } from "../lib/hydrateErrorState";
import { projectSessionAvailability } from "../lib/sessionAvailability";
import { TranscriptFollowClient } from "../lib/transcriptFollowClient";
import { initialState, reducer } from "../lib/useController";

const SUMMARY = "Failed to load conversation history.";

async function localFollowFailure(cause: unknown): Promise<unknown> {
  const client = new TranscriptFollowClient((request: FollowRequest) =>
    request.close ? Promise.resolve({ protocolVersion: 2, subscription: "", changes: [], resetRequired: false } as TranscriptFollowResponse)
      : Promise.reject(cause), { transport: "local" });
  try {
    await client.start({ install: () => {}, changes: () => {}, connection: () => {} });
  } catch (error) {
    return error;
  }
  assert.fail("follow start resolved although the host rejected the baseline read");
}

test("a failed local follow keeps its stage, reason and host cause in the recovery detail", async () => {
  const failure = await localFollowFailure(new Error("session store: database is locked"));
  const detail = hydrateFailureDetail(SUMMARY, failure);
  assert.ok(detail.startsWith(SUMMARY), detail);
  assert.match(detail, /baseline_read\.transport_rejected/);
  assert.match(detail, /session store: database is locked/);

  const failed = reducer({ ...initialState, hydrating: true }, { type: "hydrate_error", reason: "startup", error: detail });
  const availability = projectSessionAvailability({ local: failed });
  assert.deepEqual({ kind: availability.kind, source: availability.source }, { kind: "error", source: "history" });
  assert.equal(availability.detail, detail, "the banner's details show the cause, not only the summary");
});

test("causes without a follow identity keep their message; an absent cause keeps the summary alone", () => {
  assert.equal(hydrateFailureDetail(SUMMARY, new Error("resident snapshot unreadable")), `${SUMMARY}\nresident snapshot unreadable`);
  assert.equal(hydrateFailureDetail(SUMMARY, "plain rejection"), `${SUMMARY}\nplain rejection`);
  assert.equal(hydrateFailureDetail(SUMMARY, undefined), SUMMARY);
  assert.equal(hydrateFailureDetail(SUMMARY, new Error("  ")), SUMMARY);
});

test("every local history-read failure hands its cause to the recovery detail", () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), "..");
  const controller = readFileSync(join(root, "lib/useController.ts"), "utf8");
  assert.doesNotMatch(controller, /type: "hydrate_error", reason, error: t\("history\.failedLoadHistory"\) \}/,
    "a history-read failure must not replace its cause with the generic summary");
  assert.doesNotMatch(controller, /const error = t\("history\.failedLoadHistory"\);/,
    "the follow failure must not be replaced by the generic summary");
  assert.equal(controller.match(/hydrateFailureDetail\(t\("history\.failedLoadHistory"\), /g)?.length, 2,
    "both the follow path and the readable-baseline path attribute their failure");
});
