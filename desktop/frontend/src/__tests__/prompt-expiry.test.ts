import { initialState, reducer } from "../lib/useController";
import { interactionInstanceKey } from "../lib/interactionTarget";
import { sessionIdentityStableKey } from "../lib/sessionIdentity";

let passed = 0;
let failed = 0;

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) {
    passed += 1;
    process.stdout.write(`  PASS  ${label}\n`);
  } else {
    failed += 1;
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}\n`);
  }
}

const state = reducer({
  ...initialState,
  promptEpoch: 7,
  meta: { label: "", ready: true, eventChannel: "agent:event", cwd: "", session: { hostId: "local", sessionId: "session-a" }, sessionGeneration: 1 },
  ask: { id: "ask-old", questions: [] },
  mcpInteraction: { id: "mcp-new", server: "server", mode: "form", message: "new" },
  pendingPrompt: true,
}, { type: "expire_prompt", target: { tabId: "tab-a", sessionKey: sessionIdentityStableKey({ session: { hostId: "local", sessionId: "session-a" }, sessionGeneration: 1 }), hostId: "local", sessionId: "session-a", sessionGeneration: 1,
  promptId: "ask-old", kind: "ask", instanceKey: "ask-old" }, epoch: 7 });

eq(state.ask, undefined, "stale Ask expiry removes only its matching card");
eq(state.mcpInteraction?.id, "mcp-new", "stale Ask expiry preserves a newer MCP card");

const promptMeta = { label: "", ready: true, eventChannel: "agent:event", cwd: "", session: { hostId: "host-a", sessionId: "session-a" }, sessionGeneration: 3 };
const ask = { id: "ask-replay", turnId: "turn-a", runtimeEpoch: "runtime-a", questions: [] };
const sessionKey = sessionIdentityStableKey(promptMeta);
const targetBase = { tabId: "tab-a", sessionKey, hostId: "host-a", sessionId: "session-a", sessionGeneration: 3,
  promptId: ask.id, turnId: ask.turnId, runtimeEpoch: ask.runtimeEpoch, kind: "ask" as const };
const target = { ...targetBase, instanceKey: interactionInstanceKey(targetBase) };
const answered = reducer({ ...initialState, promptEpoch: 8, meta: promptMeta, ask, pendingPrompt: true },
  { type: "ask_submit_succeeded", target, epoch: 8 });
const replayed = reducer(answered, { type: "event", e: { kind: "ask_request", turnId: ask.turnId, runtimeEpoch: ask.runtimeEpoch, ask } });
eq(replayed.ask, undefined, "a delayed replay with the same host and session identity cannot resurrect an answered Ask");

console.log(`prompt expiry: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
