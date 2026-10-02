import assert from "node:assert/strict";
import { ChatContentLoader } from "../lib/chatContentLoader";
import type { Item } from "../lib/useController";
const waits: Array<(text: string) => void> = [];
let count = 0, peak = 0;
const loader = new ChatContentLoader(undefined, async () => {
  peak = Math.max(peak, ++count);
  const result = await new Promise<string>(resolve => waits.push(resolve)); count--; return result;
});
const item = (index: number): Item => ({ kind: "assistant", id: `a${index}`, text: "preview", reasoning: "thought", streaming: false });
const requests = Array.from({ length: 8 }, (_, index) => loader.load(item(index), "content"));
assert.equal(loader.load(item(0), "content"), requests[0], "identical requests share a promise");
assert.equal(waits.length, 2, "only two requests start for one session");
for (let index = 0; index < 8; index++) { waits[index](String(index)); await requests[index]; await new Promise(resolve => setImmediate(resolve)); }
assert.equal(peak, 2);
const stale = loader.load(item(0), "reasoning");
const rejection = assert.rejects(stale, /closed/);
loader.dispose(); loader.activate();
const fresh = loader.load(item(0), "reasoning");
waits[8]("old"); await rejection;
assert.equal(loader.load(item(0), "reasoning"), fresh, "old cleanup cannot remove a new generation request");
waits[9]("new"); assert.equal(await fresh, "new");
const oldRevision = loader.load(item(1), "content");
const newRevision = loader.load({ ...item(1), text: "changed" } as Item, "content");
assert.notEqual(newRevision, oldRevision, "different source revisions cannot reuse the old content request");
waits[10]("old content");
assert.equal(await oldRevision, "old content");
await new Promise(resolve => setImmediate(resolve));
waits[11]("new content");
assert.equal(await newRevision, "new content");
loader.dispose();
await new Promise(resolve => setImmediate(resolve));

const crossWaits: Array<() => void> = [];
let crossStarted = 0, globalActive = 0, globalPeak = 0, leftActive = 0, leftPeak = 0, rightActive = 0, rightPeak = 0;
const crossResolver = (side: "left" | "right") => async () => {
  crossStarted += 1;
  globalPeak = Math.max(globalPeak, ++globalActive);
  if (side === "left") leftPeak = Math.max(leftPeak, ++leftActive); else rightPeak = Math.max(rightPeak, ++rightActive);
  await new Promise<void>(resolve => crossWaits.push(resolve));
  globalActive -= 1;
  if (side === "left") leftActive -= 1; else rightActive -= 1;
  return side;
};
const left = new ChatContentLoader("left", crossResolver("left"));
const right = new ChatContentLoader("right", crossResolver("right"));
const crossRequests = [
  ...Array.from({ length: 4 }, (_, index) => left.load(item(20 + index), "content")),
  ...Array.from({ length: 4 }, (_, index) => right.load(item(30 + index), "content")),
];
assert.equal(crossWaits.length, 4, "the application starts at most four body reads");
assert.equal(leftPeak, 2, "left session stays at its two-read cap");
assert.equal(rightPeak, 2, "right session stays at its two-read cap");
while (crossStarted < crossRequests.length) {
  crossWaits.shift()?.();
  await new Promise(resolve => setImmediate(resolve));
}
for (const release of crossWaits.splice(0)) release();
await Promise.all(crossRequests);
assert.equal(globalPeak, 4, "body reads share the four-request application cap");
left.dispose(); right.dispose();
console.log("chat content: global/session concurrency, deduplication, generation and cleanup races passed");
