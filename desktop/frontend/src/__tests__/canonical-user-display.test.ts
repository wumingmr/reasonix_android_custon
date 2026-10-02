import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { canonicalMessage } from "../lib/canonicalTranscriptBackend";
import { transientUserTags } from "../lib/canonicalUserDisplay";
import { applyResolvedField, entryToRecord } from "../lib/transcriptRecordProjection";
import { historyMessagesToItems } from "../lib/useController";
import type { HistoryContentRef } from "../lib/types";

const persistent = { messageId: "user-1", position: 2, version: 1, role: "user", eventSequence: 2, visibleTurn: 1 };
const context = '<session-context version="1">\nThis host-generated snapshot supersedes every earlier session-context snapshot.\nEnvironment: private host context\n</session-context>';
const wrapped = "<response-language>prefer Chinese</response-language>\n<reasoning-language>中文</reasoning-language>\n你是谁";
const visible = (raw: Record<string, unknown>) => canonicalMessage(persistent, raw);
const items = (raw: Record<string, unknown>) => historyMessagesToItems([visible(raw)], "test").items;

// The existing Go display vocabulary remains authoritative for host wrappers.
const goSource = readFileSync(new URL("../../../../internal/agent/preview.go", import.meta.url), "utf8");
const goTags = [...goSource.match(/var TransientUserBlockTags = \[\]string\{([\s\S]*?)\n\}/)![1].matchAll(/"([\w-]+)"/g)].map(match => match[1]);
assert.deepEqual([...transientUserTags], goTags);
for (const tag of transientUserTags) {
  assert.equal(visible({ role: "user", content: `<${tag} version="1">internal</${tag}>\n你是谁` }).content, "你是谁");
}
assert.equal(visible({ role: "user", content: wrapped }).content, "你是谁");
assert.equal(visible({ role: "user", content: wrapped, raw_content: "用户原文" }).content, "用户原文");
assert.equal(visible({ role: "user", raw_content: "只有原文" }).content, "只有原文");
for (const raw of [
  { role: "user", origin: "host", content: context },
  { role: "user", origin: "host", content: "background job protocol", raw_content: "also internal" },
  { role: "user", content: context },
  { role: "user", content: "<response-language>internal only</response-language>" },
]) assert.equal(items(raw).length, 0, JSON.stringify(raw));
for (const literal of [context, "<response-language>user example</response-language>", "```xml\n" + context + "\n```", "Explain <reasoning-language> in XML"]) {
  assert.equal(visible({ role: "user", origin: "user", content: wrapped, raw_content: literal }).content, literal);
}
for (const role of ["assistant", "tool"]) assert.equal(canonicalMessage({ ...persistent, role }, { role, content: context }).content, context);
const steerPrefix = "[Mid-turn steer queued by the user. Do not treat this as a new task; use it only as additional guidance for the current task after completing the current step.]";
assert.equal(visible({ role: "user", content: `${steerPrefix}\n补充说明` }).content, "↪ 补充说明");
assert.equal(visible({ role: "user", content: `${steerPrefix}\n补充说明` }).role, "notice");
assert.equal(items({ role: "user", content: `${steerPrefix}\nA tool failed. Use read-only diagnosis as needed` }).length, 0);
const unapplied = (content: string) => canonicalMessage({ ...persistent, messageId: "unapplied", role: "tool" },
  { id: "unapplied", role: "tool", local_only: true, content });
const unappliedWithSpaces = unapplied(`${steerPrefix}\n  保留空格  `);
assert.deepEqual({ role: unappliedWithSpaces.role, content: unappliedWithSpaces.content },
  { role: "notice", content: "\n  保留空格  " });
const unappliedItem = historyMessagesToItems([unappliedWithSpaces], "test").items[0];
if (unappliedItem?.kind !== "notice") throw new Error("unapplied guidance must render as a notice");
assert.ok(unappliedItem.text.endsWith("  保留空格  "));
assert.equal(unapplied(`<response-language>zh</response-language>\n${steerPrefix}\n继续`).content, "\n继续");
assert.equal(unapplied(`${steerPrefix}\nA tool failed. Use read-only diagnosis as needed`).role, "hidden");
const compiler = `<memory-compiler-execution>${JSON.stringify({ planner_ir: { source_event: wrapped } })}</memory-compiler-execution>`;
assert.equal(visible({ role: "user", content: compiler }).content, "你是谁");
for (const suffix of ["<execution-policy>policy</execution-policy>", "<memory-recall>memory</memory-recall>"]) {
  assert.equal(visible({ role: "user", content: `你是谁\n${suffix}` }).content, "你是谁");
}
assert.equal(visible({ role: "user", content: `# Reasonix executor handoff\n\nOriginal task:\n${wrapped}\n\nPlanner output:\nprivate plan` }).content, "你是谁");

// Lazy full-body hydration must use the same projection as inline history.
for (const [origin, expected] of [["host", ""], ["user", "你是谁"]]) {
  const raw = { id: persistent.messageId, role: "user", origin, content: `<capability-route>${"x".repeat(40000)}</capability-route>\n你是谁`, raw_content: origin === "user" ? "你是谁" : undefined };
  const original = JSON.stringify(raw);
  const ref: HistoryContentRef = { entryId: "m:user-1", field: "canonicalMessage", size: original.length, chunks: 1, revision: 1, digest: "fixture" };
  const record = entryToRecord({ entryId: "m:user-1", turn: 1, order: 2, message: canonicalMessage({ ...persistent, preview: expected }, undefined), refs: [ref] });
  const data = [...new TextEncoder().encode(original)].map(byte => String.fromCharCode(byte)).join("");
  assert.equal(applyResolvedField(record, ref, data), true);
  assert.equal(record.message.content, expected);
  assert.equal(record.message.messageId, persistent.messageId);
  assert.equal(JSON.stringify(raw), original, "display never mutates source content");
  assert.equal(historyMessagesToItems([record.message], "test").items.length, origin === "host" ? 0 : 1);
}
console.log("PASS authored chat text, host filtering, literal markup, legacy wrappers, steers and lazy hydration");
