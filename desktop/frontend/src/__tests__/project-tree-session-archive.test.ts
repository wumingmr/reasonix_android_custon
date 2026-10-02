// Run: tsx src/__tests__/project-tree-session-archive.test.ts

import assert from "node:assert/strict";
import { archiveProjectTreeSession } from "../lib/projectTreeArchive";

async function main() {
  const transcript = { identity: {}, draft: "keep draft", scrollTop: 417 };
  const selectors: Array<{ sessionPath: string }> = [];
  let refreshes = 0;
  let topicNotifications = 0;
  let errors: unknown[] = [];

  const committed = await archiveProjectTreeSession({
    sessionPath: "/history/background.jsonl",
    archiveTarget: async (selector) => {
      selectors.push(selector);
      return { committed: true };
    },
    refresh: async () => { refreshes += 1; },
    topicsChanged: async () => { topicNotifications += 1; },
    showError: (error) => { errors.push(error); },
  });
  assert.equal(committed, true);
  assert.deepEqual(selectors, [{ sessionPath: "/history/background.jsonl" }]);
  assert.equal(refreshes, 1);
  assert.equal(topicNotifications, 1);
  assert.equal(errors.length, 0);
  assert.deepEqual(transcript, { identity: {}, draft: "keep draft", scrollTop: 417 });

  const structured = { data: { sessionCode: "operation_busy" } };
  const rejected = await archiveProjectTreeSession({
    sessionPath: "/history/busy.jsonl",
    archiveTarget: async () => { throw structured; },
    refresh: async () => { refreshes += 1; },
    showError: (error) => { errors.push(error); },
  });
  assert.equal(rejected, false);
  assert.equal(refreshes, 2, "a rejected archive reconciles only the project tree");
  assert.deepEqual(errors, [structured]);
  assert.deepEqual(transcript, { identity: {}, draft: "keep draft", scrollTop: 417 });

  process.stdout.write("PASS explicit session archive routing and current transcript stability\n");
}

void main();
