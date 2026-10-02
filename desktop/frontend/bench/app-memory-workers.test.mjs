import assert from "node:assert/strict";
import { test } from "node:test";
import { workerListeners } from "./app-memory-workers.mjs";

test("Worker inspection releases all remote references, including on failure", async () => {
  for (const fail of [false, true]) {
    const calls = [];
    const cdp = { async send(method) {
      calls.push(method);
      if (method === "Runtime.evaluate") return { result: { objectId: "prototype" } };
      if (method === "Runtime.queryObjects") return { objects: { objectId: "workers" } };
      if (method === "Runtime.getProperties") return { result: [{ name: "0", value: { objectId: "worker" } }, { name: "length", value: { value: 1 } }] };
      if (method === "DOMDebugger.getEventListeners") {
        if (fail) throw new Error("inspection failed");
        return { listeners: [{ type: "message" }, { type: "error" }, { type: "error" }] };
      }
    } };
    if (fail) await assert.rejects(workerListeners(cdp), /inspection failed/);
    else assert.deepEqual(await workerListeners(cdp), [["error", "error", "message"]]);
    assert.equal(calls.at(-1), "Runtime.releaseObjectGroup");
  }
});
