import assert from "node:assert/strict";
import { releaseMediaElement } from "../components/WorkspaceMediaPreview";

const calls: string[] = [];
releaseMediaElement({
  pause: () => { calls.push("pause"); },
  removeAttribute: (name) => { calls.push(`remove:${name}`); },
  load: () => { calls.push("load"); },
});
assert.deepEqual(calls, ["pause", "remove:src", "load"], "media cleanup stops decode and releases its source");
console.log("workspace media release: pause, detach source and decode reset passed");
