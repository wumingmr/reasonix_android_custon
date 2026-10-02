import assert from "node:assert/strict";
import { sessionTitleTarget, sessionTitleErrorKey, sessionTitleSelector } from "../lib/sessionTitleOperation";
import type { ProjectNode } from "../lib/types";

const first: ProjectNode = { key: "first", kind: "topic", label: "First", topicId: "shared", sessionPath: "legacy", session: { hostId: "local", sessionId: "first" } };
const second = { ...first, key: "second", session: { hostId: "local", sessionId: "second" } };
assert.equal(sessionTitleTarget(first), "session-id:first");
assert.notEqual(sessionTitleTarget(first), sessionTitleTarget(second));
assert.equal(sessionTitleTarget({ ...first, session: undefined }), "legacy");
assert.equal(sessionTitleTarget({ ...first, session: undefined, sessionPath: undefined }), "shared");
assert.deepEqual(sessionTitleSelector("session-id:first"), { ref: { hostId: "local", sessionId: "first" } });
assert.deepEqual(sessionTitleSelector("/tmp/session.jsonl"), { sessionPath: "/tmp/session.jsonl" });
assert.deepEqual(sessionTitleSelector("shared"), { topicId: "shared" });
assert.equal(sessionTitleErrorKey(new Error("session_operation:title_conflict:internal details")), "projectTree.sessionError.titleConflict");
assert.equal(sessionTitleErrorKey({ data: { sessionCode: "provider_unavailable" }, message: "secret provider body" }), "projectTree.sessionError.providerUnavailable");
assert.equal(sessionTitleErrorKey({ data: { sessionCode: "session_damaged" }, message: "session_operation:session_damaged:detail" }), "projectTree.sessionError.damaged");
assert.equal(sessionTitleErrorKey(new Error("session_operation:session_damaged:This session's saved file is damaged")), "projectTree.sessionError.damaged");
for (const error of [new Error("failed to read /private/fixture/session"), "lease owner controller test-owner", "provider secret test-token"]) {
  assert.equal(sessionTitleErrorKey(error), "projectTree.sessionError.failed");
}
console.log("session title target precedence, sibling isolation and sanitized errors passed");
