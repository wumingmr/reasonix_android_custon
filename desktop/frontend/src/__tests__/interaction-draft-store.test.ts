import assert from "node:assert/strict";
import { createInteractionDraftStore } from "../lib/interactionDraftStore";

const store = createInteractionDraftStore<number>(2);
store.write("session-a\u0000ask-1", 1);
store.write("session-a\u0000ask-2", 2);
assert.equal(store.read("session-a\u0000ask-1"), 1);
store.write("session-b\u0000ask-1", 3);
assert.equal(store.read("session-a\u0000ask-2"), undefined, "least-recent request is evicted at the bound");
assert.equal(store.read("session-a\u0000ask-1"), 1, "same request survives component remount");
assert.equal(store.read("session-b\u0000ask-1"), 3, "reused prompt ids remain scoped by session");
store.releaseScope("session-a");
assert.equal(store.read("session-a\u0000ask-1"), undefined, "session release removes only its drafts");
assert.equal(store.read("session-b\u0000ask-1"), 3);
console.log("interaction draft store: bounded request identity and session release passed");
