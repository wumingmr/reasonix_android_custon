// Run: pnpm exec tsx src/__tests__/provider-failure-meta.test.ts
import assert from "node:assert/strict";

import { initialState, reducer } from "../lib/useController";
import { historyMessagesToItems } from "../lib/historyItems";
import { historyNoticeItems } from "../lib/controllerNotices";
import { presentError } from "../lib/errorPresentation";
import { en } from "../locales/en";
import type { HistoryMessage } from "../lib/types";

const started = reducer(initialState, { type: "event", e: { kind: "turn_started" } });
const failed = reducer(started, {
  type: "event",
  e: {
    kind: "turn_done",
    err: "Deepseek2 · Chat Completions: Request endpoint not found (HTTP 404).",
    detail: "Connection ID: deepseek-anthropic\nRequest path: /anthropic/v1/chat/completions",
    diagnostic: { kind: "request", status: 404, providerId: "deepseek-anthropic", providerDisplayName: "Deepseek2", protocol: "openai", requestPath: "/anthropic/v1/chat/completions" },
  },
});
const notice = failed.items.find((item) => item.kind === "notice" && item.level === "warn");

assert.equal(
  notice?.kind === "notice" ? notice.text : "",
  "Deepseek2 · Chat Completions: Request endpoint not found (HTTP 404).",
  "provider display identity stays in the primary live error",
);
assert.equal(
  notice?.kind === "notice" ? notice.detail : "",
  "Connection ID: deepseek-anthropic\nRequest path: /anthropic/v1/chat/completions",
  "stable provider id and sanitized path stay in live diagnostic details",
);

const diagnostic = { kind: "transport_protocol", transportCode: "PROTOCOL_ERROR" };
const history: HistoryMessage = { role: "notice", content: "opaque provider failure", level: "warn", diagnostic };
const live = reducer(started, { type: "event", e: { kind: "turn_done", err: history.content, diagnostic } });
const interrupted = reducer(started, { type: "event", e: { kind: "turn_done", status: "interrupted", err: history.content, diagnostic } });
const liveNotice = reducer(started, { type: "event", e: { kind: "notice", level: "warn", text: history.content, diagnostic } });
for (const items of [live.items, interrupted.items, liveNotice.items, historyMessagesToItems([history], "full").items, historyNoticeItems(history, "windowed")]) {
  const item = items.find(item => item.kind === "notice" && item.level === "warn");
  assert.ok(item?.kind === "notice");
  assert.deepEqual(item.diagnostic, diagnostic, "live and both history projections retain structured evidence");
  const presentation = presentError(item.text, key => en[key], "en", item.diagnostic);
  assert.equal(presentation.summary, en["error.transportProtocol"]);
  assert.match(presentation.detail, /PROTOCOL_ERROR/);
}
