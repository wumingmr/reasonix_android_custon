import assert from "node:assert/strict";
import { browserElementDrafts } from "../lib/browserElementDrafts";
import { attachBrowserComposer } from "../app-runtime/browserComposerBridge";

const original: string[] = [], other: string[] = [];
browserElementDrafts.setState({ target: undefined, pending: {} });
const closeOriginal = attachBrowserComposer("task", "session-old", text => original.push(text));
const captured = browserElementDrafts.getState().target!;
closeOriginal();
const closeOther = attachBrowserComposer("task", "session-new", text => other.push(text));
// A picker begun in the old session resolves after navigation to a new session.
browserElementDrafts.getState().add(captured.taskId, captured.sessionId, { role: "button", name: "Save", epoch: 3 });
assert.deepEqual(other, [], "late selection must never enter the new session draft");
assert.equal(Object.keys(browserElementDrafts.getState().pending).length, 1);
closeOther();
const closeRestored = attachBrowserComposer("task", "session-old", text => original.push(text));
assert.equal(original.length, 1);
assert.equal(JSON.parse(original[0]).executableReference, false);
assert.equal(JSON.parse(original[0]).browserElement.name, "Save");
assert.equal(Object.keys(browserElementDrafts.getState().pending).length, 0);
closeRestored();
const closeAgain = attachBrowserComposer("task", "session-old", text => original.push(text));
assert.equal(original.length, 1, "returning to the session must not duplicate an attachment");
closeAgain();
console.log("browser element drafts: late selection stays with its original session and is delivered once");
