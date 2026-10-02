import assert from "node:assert/strict";
import { retainPreparedSessionLabels, sessionIsBlank, sessionMetadataPending } from "../lib/workspaceSessionPresentation";
import type { WorkspaceSessionSummary } from "../generated/desktopContract.generated";

const row = { ref: { hostId: "local", sessionId: "existing" }, title: "Previous authored title", preview: "preview", blank: false, metadataStatus: "ready" } as WorkspaceSessionSummary;
const pending = { ...row, title: "", preview: "", blank: true, metadataStatus: "pending" };
assert.equal(sessionIsBlank(pending), false);
assert.equal(sessionMetadataPending(pending), true);
assert.equal(retainPreparedSessionLabels([pending], [row])[0].title, row.title);
assert.equal(retainPreparedSessionLabels([{ ...pending, ref: { ...row.ref, sessionId: "other" } }], [row])[0].title, "");
assert.equal(retainPreparedSessionLabels([{ ...pending, metadataStatus: "ready" }], [row])[0].title, "");
assert.equal(sessionIsBlank({ ...pending, metadataStatus: "ready" }), true);
assert.equal(sessionIsBlank({ ...pending, metadataStatus: "failed" }), false);
console.log("PASS pending vs blank, retained labels, session isolation and authoritative empty metadata");
