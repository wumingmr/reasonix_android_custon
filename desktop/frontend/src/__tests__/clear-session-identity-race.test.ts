// Run: npx tsx src/__tests__/clear-session-identity-race.test.ts
//
// Deterministic race barriers for clear-session identity fencing.
// Avoids importing useController (React) so the suite runs without node_modules.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { hydrateIdentityCurrent } from "../lib/sessionIdentity";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

console.log("\nclear-session identity race");

// ── hydrate identity fence (used by loadSessionDataForTab.stillCurrent) ─────
ok(hydrateIdentityCurrent({ sessionPath: "/a.jsonl", sessionGeneration: 1 }, { sessionPath: "/a.jsonl", sessionGeneration: 1 }), "matching path+generation is current");
ok(!hydrateIdentityCurrent({ sessionPath: "/a.jsonl", sessionGeneration: 1 }, { sessionPath: "/b.jsonl", sessionGeneration: 2 }), "path drift after clear is rejected");
ok(!hydrateIdentityCurrent({ sessionPath: "/a.jsonl", sessionGeneration: 1 }, { sessionPath: "/a.jsonl", sessionGeneration: 2 }), "generation-only drift after clear is rejected");
ok(hydrateIdentityCurrent({}, { sessionPath: "/b.jsonl", sessionGeneration: 2 }), "empty load identity does not false-reject");
ok(!hydrateIdentityCurrent(
  { session: { hostId: "local", sessionId: "canonical-a" }, sessionGeneration: 1 },
  { session: { hostId: "local", sessionId: "canonical-b" }, sessionGeneration: 1 },
), "canonical identity drift is rejected when session paths are empty");

// ── deferred A hydrate vs clear→B (barrier interleaving) ───────────────────
type LiveMeta = { sessionPath: string; sessionGeneration: number; items: string[] };

const live: LiveMeta = {
  sessionPath: "/sessions/a.jsonl",
  sessionGeneration: 1,
  items: ["old content from A"],
};

const lateA = deferred<{ path: string; generation: number; items: string[] }>();

// Hydrate A starts and hangs (simulates HistorySliceForTab in flight).
const hydrateA = (async () => {
  const loadPath = live.sessionPath;
  const loadGen = live.sessionGeneration;
  const page = await lateA.promise;
  // stillCurrent check at apply time — equivalent to useController fence.
  if (!hydrateIdentityCurrent(
    { sessionPath: loadPath, sessionGeneration: loadGen },
    { sessionPath: live.sessionPath, sessionGeneration: live.sessionGeneration },
  )) {
    return { applied: false as const, items: live.items.slice() };
  }
  live.items = page.items;
  return { applied: true as const, items: live.items.slice() };
})();

// Clear succeeds: rotate identity to B and wipe transcript (atomic clear path).
live.sessionPath = "/sessions/b.jsonl";
live.sessionGeneration = 2;
live.items = [];

// Immediate mode switch would start hydrate B; A is still pending.
ok(live.items.length === 0, "after clear, transcript is empty before late A returns");
ok(live.sessionGeneration === 2, "after clear, generation is B");

// Late A resolves with old content.
lateA.resolve({
  path: "/sessions/a.jsonl",
  generation: 1,
  items: ["old content from A"],
});

const result = await hydrateA;
ok(result.applied === false, "late A hydrate is not applied after clear to B");
ok(result.items.length === 0, "transcript remains empty after rejected late A");
ok(live.sessionPath === "/sessions/b.jsonl", "live identity path stays B");
ok(live.sessionGeneration === 2, "live identity generation stays B");

// Source contract: clearSession still wires the real fence pieces.
const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const controller = readFileSync(join(root, "lib/useController.ts"), "utf8");
assert.match(controller, /hydrateIdentityCurrent\(/, "useController uses shared identity fence");
assert.match(controller, /evictTab\(tabId\)/, "clearSession evicts TranscriptStore");
assert.match(controller, /sessionGeneration:\s*cleared\.sessionGeneration/, "clear applies returned generation");
assert.match(
  controller,
  /sessionIdentityStableKey\(a\) === sessionIdentityStableKey\(b\)/,
  "sameMeta compares the complete canonical identity through the shared helper",
);
assert.match(controller, /a\.sessionGeneration === b\.sessionGeneration/, "sameMeta also fences unbound generations");

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
