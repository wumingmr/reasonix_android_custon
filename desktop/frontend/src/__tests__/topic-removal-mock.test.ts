import assert from "node:assert/strict";
import { inspect, remove, markTopic, trash, recover } from "../lib/topicRemovalMock";
import { makeMockSessionLifecycleBindings } from "../lib/sessionLifecycleBindings";
import type { ProjectNode } from "../lib/types";

const tree: ProjectNode[] = [{ key: "global", kind: "global_folder", label: "Global", children: [
  { key: "empty", topicId: "empty", kind: "global_topic", label: "New conversation" },
  { key: "named", topicId: "named", kind: "global_topic", label: "My plan" },
] }];
markTopic(tree, "empty", true);
const target = { workspaceId: "global", topicId: "empty" };
let view = inspect(tree, target);
assert.equal(view.disposition, "discard_placeholder");
assert.ok(remove(tree, { operationId: "empty", target, expectedToken: view.token }).committed);
assert.equal(trash(tree).items.length, 0);
view = inspect(tree, { ...target, topicId: "named" });
const request = { operationId: "named", target: view.target, expectedToken: view.token };
tree[0].children![0].label = "Renamed";
assert.equal(remove(tree, request).errorCode, "state_conflict");
view = inspect(tree, request.target);
request.expectedToken = view.token;
const result = remove(tree, request);
assert.ok(result.committed);
assert.equal(trash(tree).items[0].canPreview, false);
recover(tree, result.recoveryEntryId!, "global", "restore");
assert.equal(trash(tree).items.length, 0);
assert.equal(tree[0].children![0].label, "Renamed");
assert.deepEqual(remove(tree, request), result);
assert.equal(tree[0].children!.length, 1);
markTopic(tree, "named", false);
tree[0].children![0].label = "New conversation";
assert.equal(inspect(tree, request.target).disposition, "archive_placeholder");
const bindings = { ...makeMockSessionLifecycleBindings(
  () => { throw new Error("topic removal must use its own project tree"); },
  new Set(), new Set(), () => {}, tree,
) };
assert.equal((await bindings.InspectTopicRemoval(request.target)).disposition, "archive_placeholder");
assert.deepEqual(await bindings.RemoveTopic(request), result, "lazy bridge retains the same removal receipt");
assert.equal(tree[0].children!.length, 1, "lazy duplicate cannot remove the restored topic");
console.log("PASS mock placeholder classification, recovery and idempotency");
