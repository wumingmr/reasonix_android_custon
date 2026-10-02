import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { CompactionCard } from "../components/TranscriptCards";
import { LocaleProvider } from "../lib/i18n";
import type { Item } from "../lib/useController";

type CompactionItem = Extract<Item, { kind: "compaction" }>;

const item = (status: string, pending: boolean, extra: Partial<CompactionItem> = {}): CompactionItem => ({
  kind: "compaction", id: "maintenance:card", pending, trigger: "manual", messages: 8,
  summary: "durable summary", archive: "", operationId: "card", status, ...extra,
});
const render = (value: CompactionItem) => renderToStaticMarkup(
  <LocaleProvider><CompactionCard item={value} /></LocaleProvider>,
);

const running = render(item("running", true));
assert.match(running, /class="compaction compaction--pending"/);
assert.match(running, /Compacting conversation/);
assert.match(running, /Generating summary/);
assert.match(running, /compaction__spinner/);
assert.match(running, /role="status"/);
assert.doesNotMatch(running, /<button/);

const cancelling = render(item("cancelling", true));
assert.match(cancelling, /Stopping compaction/);
assert.doesNotMatch(cancelling, /Generating a summary/);

const saving = render(item("finalizing", true));
assert.match(saving, /Saving compaction result/);

const confirming = render(item("confirming", true));
assert.match(confirming, /Checking compaction status/);

const completed = render(item("completed", false, { applied: true, inputTokens: 1200, resultTokens: 400 }));
assert.match(completed, /<button/);
assert.match(completed, /1200.*400/);
assert.match(completed, /aria-expanded="false"/);

const failed = render(item("failed", false, { errorCode: "save_failed", detail: "disk full", applied: true }));
assert.match(failed, /Compaction failed/);
assert.match(failed, /<button/);

const noop = render(item("noop", false));
assert.match(noop, /No history to compact/);
assert.doesNotMatch(noop, /compaction__spinner/);
assert.doesNotMatch(noop, /<button/);

const unknown = render(item("future_state", false));
assert.doesNotMatch(unknown, /Context compacted/);
assert.match(unknown, /could not be fully restored/);

console.log("maintenance card: running, stopping, saving, terminal and no-history styles passed");
