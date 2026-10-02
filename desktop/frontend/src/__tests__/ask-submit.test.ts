// Run: tsx src/__tests__/ask-submit.test.ts

import type { AppBindings } from "../lib/bridge";
import { answerPromptForActiveTurn } from "../lib/inboxSubmit";
import type { QuestionAnswer } from "../lib/types";

let passed = 0;
let failed = 0;

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}\n`);
    failed += 1;
  }
}

function binding(overrides: Partial<AppBindings>): AppBindings {
  return overrides as AppBindings;
}

const answers: QuestionAnswer[] = [{ questionId: "q1", selected: ["yes"] }];
console.log("\nAsk prompt exact-request submission");

{
  let pendingCalls = 0;
  const exactCalls: string[] = [];
  await answerPromptForActiveTurn(binding({
    PendingPromptIdentitiesForTab: async () => { pendingCalls += 1; return []; },
    ResolvePromptForTab: async (tabId, promptId, turnId) => { exactCalls.push(`${tabId}:${turnId}:${promptId}`); },
  }), "tab-ask", "ask-1", answers, "turn-known");
  eq(pendingCalls, 0, "complete Ask identity does not query the current turn");
  eq(exactCalls.join("|"), "tab-ask:turn-known:ask-1", "the card's original turn fences the exact answer");
}

{
  let pendingCalls = 0;
  const exactCalls: string[] = [];
  await answerPromptForActiveTurn(binding({
    PendingPromptIdentitiesForTab: async () => { pendingCalls += 1; return [{ promptId: "ask-2", turnId: "turn-original", runtimeEpoch: "runtime-a", kind: "ask" }]; },
    ResolvePromptForTab: async (tabId, promptId, turnId) => { exactCalls.push(`${tabId}:${turnId}:${promptId}`); },
  }), "tab-ask", "ask-2", answers);
  eq(pendingCalls, 1, "missing local identity queries pending prompts once");
  eq(exactCalls.join("|"), "tab-ask:turn-original:ask-2", "only the matching pending Ask can complete identity");
}

{
  let exactCalls = 0;
  let rejected = "";
  try {
    await answerPromptForActiveTurn(binding({
      PendingPromptIdentitiesForTab: async () => [],
      ResolvePromptForTab: async () => { exactCalls += 1; },
    }), "tab-ask", "ask-3", answers);
  } catch (error) {
    rejected = error instanceof Error ? error.message : String(error);
  }
  eq(rejected.includes("stale") || rejected.includes("exact identity"), true, "missing exact prompt identity rejects visibly");
  eq(exactCalls, 0, "missing turn id never calls the exact endpoint with an empty fence");
}

{
  let exactCalls = 0;
  let rejected = "";
  try {
    await answerPromptForActiveTurn(binding({
      PendingPromptIdentitiesForTab: async () => { throw new Error("pending lookup failed"); },
      ResolvePromptForTab: async () => { exactCalls += 1; },
    }), "tab-ask", "ask-rpc", answers);
  } catch (error) {
    rejected = error instanceof Error ? error.message : String(error);
  }
  eq(rejected, "pending lookup failed", "pending identity lookup failure propagates");
  eq(exactCalls, 0, "failed turn lookup never calls the exact endpoint");
}

{
  let rejected = "";
  try {
    await answerPromptForActiveTurn(binding({
      ResolvePromptForTab: async () => { throw new Error("stale turn"); },
    }), "tab-ask", "ask-4", answers, "turn-original", "runtime-a");
  } catch (error) {
    rejected = error instanceof Error ? error.message : String(error);
  }
  eq(rejected, "stale turn", "exact endpoint rejection propagates to the decision surface");
}

{
  let rejected = "";
  try {
    await answerPromptForActiveTurn(binding({}), "tab-ask", "ask-legacy", answers, "turn-original", "runtime-a");
  } catch (error) {
    rejected = error instanceof Error ? error.message : String(error);
  }
  eq(rejected.includes("upgrade the host"), true, "missing exact prompt API fails closed");
}

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
