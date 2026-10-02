import assert from "node:assert/strict";
import { test } from "node:test";
import { markSessionRead, mergeReadStores, repairReadBaseline, seedSessionReads, type ReadStore } from "../lib/sessionReadActivity";
import { projectTreeReadActivityKey } from "../lib/projectTreeTopic";
import type { ProjectNode } from "../lib/types";
const node: ProjectNode = { key: "b", kind: "topic", label: "B", session: { hostId: "local", sessionId: "b" }, resultSequence: 20 };
const key = projectTreeReadActivityKey(node)!;
const polluted = (): ReadStore => ({ version: 3, baselineAt: 1, records: { [key]: { metric: "result", value: 100, revision: 0, imported: true } } });
test("only verified imported baseline is repaired, once", () => {
  const store = polluted(), captured = store.records[key];
  assert.equal(repairReadBaseline(store, key, captured, { complete: false, resultSequence: 10, eventVersion: "30" }), store);
  const fixed = repairReadBaseline(store, key, captured, { complete: true, resultSequence: 10, eventVersion: "30" });
  assert.equal(fixed.records[key].value, 10);
  assert.equal(repairReadBaseline(fixed, key, fixed.records[key], { complete: true, resultSequence: 5, eventVersion: "31" }), fixed);
  const read = markSessionRead(fixed, node);
  assert.equal(read.records[key].value, 20);
  assert.equal(markSessionRead(read, { ...node, resultSequence: 10 }), read);
});
test("user read invalidates an outstanding pollution repair", () => {
  const store = polluted(), captured = store.records[key];
  const read = markSessionRead(store, node);
  assert.equal(repairReadBaseline(read, key, captured, { complete: true, resultSequence: 10, eventVersion: "30" }), read);
  assert.equal(repairReadBaseline(read, key, read.records[key], { complete: true, resultSequence: 10, eventVersion: "30" }), read);
  assert.equal(repairReadBaseline(read, key, read.records[key], { complete: true, resultSequence: 20, eventVersion: "60" }).records[key].value,20);
});
test("cross-window merges accept exact repair but never lower a newer user read", () => {
  const old = polluted();
  const fixed = repairReadBaseline(old, key, old.records[key], { complete: true, resultSequence: 10, eventVersion: "30" });
  assert.equal(mergeReadStores(old, fixed).records[key].value, 10);
  assert.equal(mergeReadStores(fixed, old).records[key].value, 10);
  const read = markSessionRead(old, node);
  assert.equal(mergeReadStores(read, fixed).records[key].value, 100);
});

test("adoption changes time to result only after an authoritative baseline", () => {
  const source = "source\0local\0old-head";
  const store: ReadStore = { version: 3, baselineAt: 1, records: { [source]: { metric: "time", value: 100000, revision: 1 } } };
  const adopted = { ...node, turnsState: "ready", identityAliases: [source], resultSequence: 10 };
  const seeded = seedSessionReads(store, [adopted]);
  assert.equal(seeded.records[key].needsBaseline, true);
  assert.equal(seeded.records[key].value, 0);
  const verified = repairReadBaseline(seeded, key, seeded.records[key], { complete: true, resultSequence: 20, eventVersion: "60" });
  assert.equal(verified.records[key].value, 20);
  assert.equal(verified.records[key].needsBaseline, false);
  const read = markSessionRead(seeded, { ...adopted, resultSequence: 30 });
  assert.equal(repairReadBaseline(read, key, seeded.records[key], { complete: true, resultSequence: 20, eventVersion: "60" }), read);
});

test("a concurrent user read survives a repair delivered first", () => {
  const old = polluted();
  const fixed = repairReadBaseline(old, key, old.records[key], { complete: true, resultSequence: 10, eventVersion: "30" });
  const read = markSessionRead(old, node);
  assert.equal(mergeReadStores(fixed, read).records[key].value, 20);
});
