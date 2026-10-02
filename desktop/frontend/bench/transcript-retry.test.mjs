import assert from "node:assert/strict";
import { test } from "node:test";
import { launchTranscriptRetryHost } from "./transcript-retry.mjs";

function fakeEngine(events) {
  return {
    async launch(options) {
      events.push(["launch", options]);
      return {
        async newContext(contextOptions) {
          events.push(["newContext", contextOptions]);
          return {
            async newPage() {
              events.push(["newPage"]);
              return { id: events.length };
            },
            async close() {
              events.push(["closeContext"]);
            },
          };
        },
        async close() {
          events.push(["closeBrowser"]);
        },
      };
    },
  };
}

test("bounded retries own an independent browser and release each context", async () => {
  const events = [];
  const host = await launchTranscriptRetryHost(fakeEngine(events), {
    headless: true,
    viewport: { width: 1280, height: 900 },
  });
  assert.equal(await host.run(async page => page.id), 3);
  await assert.rejects(() => host.run(async () => { throw new Error("sample failed"); }), /sample failed/);
  await host.close();
  await host.close();
  assert.deepEqual(events, [
    ["launch", { headless: true }],
    ["newContext", { viewport: { width: 1280, height: 900 } }],
    ["newPage"],
    ["closeContext"],
    ["newContext", { viewport: { width: 1280, height: 900 } }],
    ["newPage"],
    ["closeContext"],
    ["closeBrowser"],
  ]);
  await assert.rejects(() => host.run(async () => undefined), /host is closed/);
});
