import assert from "node:assert/strict";
import { test } from "node:test";
import { CaptureQueue, abortable } from "./captureQueue.js";

test("an already-cancelled wait still consumes the underlying promise rejection", async () => {
  const controller = new AbortController(); controller.abort();
  let reject!: (reason: Error) => void;
  const work = new Promise<void>((_resolve, fail) => { reject = fail; });
  await assert.rejects(abortable(work, controller.signal), /cancelled/);
  reject(new Error("late page evaluation failure"));
  await new Promise<void>(resolve => setImmediate(resolve));
});

test("cancelled middle waiter cannot release an earlier capture or unblock its successor", async () => {
  const queue = new CaptureQueue();
  let finish!: () => void;
  const first = queue.run(new AbortController().signal, () => new Promise<void>(resolve => { finish = resolve; }));
  await Promise.resolve();
  const controller = new AbortController();
  const second = queue.run(controller.signal, async () => assert.fail("cancelled waiter ran"));
  let thirdEntered = false;
  const third = queue.run(new AbortController().signal, async () => { thirdEntered = true; });
  controller.abort();
  await assert.rejects(second, /cancelled/);
  assert.equal(thirdEntered, false);
  finish();
  await Promise.all([first, third]);
  assert.equal(thirdEntered, true);
});

test("failure releases its slot exactly once", async () => {
  const queue = new CaptureQueue();
  await assert.rejects(queue.run(new AbortController().signal, async () => { throw new Error("probe failed"); }), /probe failed/);
  assert.equal(await queue.run(new AbortController().signal, async () => 42), 42);
});
